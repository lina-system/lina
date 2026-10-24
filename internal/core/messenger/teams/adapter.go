// SPDX-License-Identifier: Apache-2.0
// Package teams reads work and school Teams accounts through Microsoft Graph.
package teams

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/kit/secret"
)

type Config struct {
	Tenant, Account string
	Token           func(context.Context, bool) (secret.Secret, error)
	Transport       http.RoundTripper
}
type Adapter struct {
	cfg      Config
	client   *http.Client
	wait     func(context.Context, time.Duration) error
	profiles map[string]messenger.Author
}

func NewAdapter(cfg Config) *Adapter {
	return &Adapter{cfg: cfg, client: client(cfg.Transport), wait: pause, profiles: map[string]messenger.Author{}}
}

var _ messenger.Adapter = (*Adapter)(nil)

func (*Adapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{HistoryDepth: "Graph retained history", CursorKind: "Graph nextLink and modification watermark", Edits: true, Deletions: true, Interval: time.Minute}
}
func (*Adapter) Live(context.Context, func(messenger.Event) error) error { return nil }
func (*Adapter) FetchAttachment(context.Context, string, string) (io.ReadCloser, error) {
	return nil, ErrUnsupported
}
func (a *Adapter) SignIn(ctx context.Context) (messenger.SignInState, error) {
	_, err := a.cfg.Token(ctx, false)
	if err != nil {
		return messenger.NeedsSignIn, err
	}
	return messenger.SignedIn, nil
}
func segment(s string) string { return url.PathEscape(s) }
func chatPath(id string) (string, bool, error) {
	if strings.HasPrefix(id, "chat:") {
		v := strings.TrimPrefix(id, "chat:")
		if v != "" && !strings.ContainsAny(v, "/\\") {
			return "/chats/" + segment(v), false, nil
		}
	}
	if strings.HasPrefix(id, "channel:") {
		v := strings.SplitN(strings.TrimPrefix(id, "channel:"), ":", 2)
		if len(v) == 2 && v[0] != "" && v[1] != "" && !strings.ContainsAny(v[0]+v[1], "/\\") {
			return "/teams/" + segment(v[0]) + "/channels/" + segment(v[1]), true, nil
		}
	}
	return "", false, ErrInvalid
}
func (a *Adapter) ListChats(ctx context.Context) ([]messenger.Chat, error) {
	out := []messenger.Chat{}
	err := a.pages(ctx, graph+"/me/chats?$top=50", func(raw json.RawMessage) error {
		var c struct{ ID, Topic, ChatType string }
		if json.Unmarshal(raw, &c) != nil || c.ID == "" {
			return ErrInvalid
		}
		kind := "group"
		if c.ChatType == "oneOnOne" {
			kind = "direct"
		}
		out = append(out, messenger.Chat{ID: "chat:" + c.ID, Name: c.Topic, Kind: kind})
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = a.pages(ctx, graph+"/me/joinedTeams", func(raw json.RawMessage) error {
		var team struct{ ID, DisplayName string }
		if json.Unmarshal(raw, &team) != nil || team.ID == "" {
			return ErrInvalid
		}
		return a.pages(ctx, graph+"/teams/"+segment(team.ID)+"/channels", func(raw json.RawMessage) error {
			var c struct{ ID, DisplayName string }
			if json.Unmarshal(raw, &c) != nil || c.ID == "" {
				return ErrInvalid
			}
			out = append(out, messenger.Chat{ID: "channel:" + team.ID + ":" + c.ID, Name: team.DisplayName + " / " + c.DisplayName, Kind: "channel"})
			return nil
		})
	})
	return out, err
}
func (a *Adapter) Members(ctx context.Context, id string) ([]messenger.Author, error) {
	path, _, err := chatPath(id)
	if err != nil {
		return nil, err
	}
	out := []messenger.Author{}
	err = a.pages(ctx, graph+path+"/members", func(raw json.RawMessage) error {
		var m struct{ UserID, TenantID, DisplayName, Email string }
		if json.Unmarshal(raw, &m) != nil || m.UserID == "" {
			return ErrInvalid
		}
		if m.TenantID == "" {
			m.TenantID = a.cfg.Tenant
		}
		author := messenger.Author{ID: m.TenantID + ":" + m.UserID, Name: m.DisplayName, Email: m.Email}
		a.profiles[author.ID] = author
		out = append(out, author)
		return nil
	})
	return out, err
}

type graphMessage struct {
	ID, ReplyToID, MessageType string
	Created                    time.Time  `json:"createdDateTime"`
	Modified                   time.Time  `json:"lastModifiedDateTime"`
	Deleted                    *time.Time `json:"deletedDateTime"`
	From                       struct {
		User        *struct{ ID, DisplayName, TenantID string }
		Application *struct{ ID, DisplayName string }
	}
	Body        struct{ Content, ContentType string }
	Attachments []struct{ ID, Name, ContentType string }
}
type cursor struct {
	Pages int       `json:"pages,omitempty"`
	Chat  string    `json:"chat"`
	Next  string    `json:"next,omitempty"`
	Since time.Time `json:"since"`
	High  time.Time `json:"high"`
}

func (a *Adapter) Page(ctx context.Context, id string, position messenger.Cursor) (messenger.Page, error) {
	path, channel, err := chatPath(id)
	if err != nil {
		return messenger.Page{}, err
	}
	path += "/messages"
	c := cursor{Chat: id}
	if position != "" && (json.Unmarshal([]byte(position), &c) != nil || c.Chat != id) {
		return messenger.Page{}, ErrInvalid
	}
	if c.Pages >= 10000 || len(position) > 65536 {
		return messenger.Page{}, ErrInvalid
	}
	c.Pages++
	target := graph + path + "?$top=50"
	if !channel {
		q := url.Values{"$top": {"50"}, "$orderby": {"lastModifiedDateTime desc"}}
		if !c.Since.IsZero() {
			q.Set("$filter", "lastModifiedDateTime gt "+c.Since.Add(-time.Second).UTC().Format(time.RFC3339Nano))
		}
		target = graph + path + "?" + q.Encode()
	}
	parsed, _ := url.Parse(target)
	if c.Next != "" {
		if err = nextURL(c.Next, parsed.Path); err != nil {
			return messenger.Page{}, err
		}
		target = c.Next
	}
	var wire struct {
		Value []graphMessage `json:"value"`
		Next  string         `json:"@odata.nextLink"`
	}
	if err = a.get(ctx, target, &wire); err != nil {
		return messenger.Page{}, err
	}
	if wire.Value == nil {
		return messenger.Page{}, ErrInvalid
	}
	if err = nextURL(wire.Next, parsed.Path); err != nil {
		return messenger.Page{}, err
	}
	p := messenger.Page{}
	add := func(m graphMessage) error {
		if m.ID == "" {
			return ErrInvalid
		}
		modified := m.Modified
		if modified.IsZero() {
			modified = m.Created
		}
		if m.Deleted != nil && m.Deleted.After(modified) {
			modified = *m.Deleted
		}
		if modified.After(c.High) {
			c.High = modified
		}
		if channel && !c.Since.IsZero() && modified.Before(c.Since.Add(-time.Second)) {
			return nil
		}
		if m.Deleted != nil && !m.Deleted.IsZero() {
			p.Deletions = append(p.Deletions, messenger.Deletion{Message: m.ID, At: *m.Deleted})
			return nil
		}
		normalized, err := a.message(ctx, m)
		if err != nil {
			return err
		}
		p.Messages = append(p.Messages, normalized)
		return nil
	}
	for _, m := range wire.Value {
		if err = add(m); err != nil {
			return messenger.Page{}, err
		}
		if channel {
			err = a.pages(ctx, graph+path+"/"+segment(m.ID)+"/replies?$top=50", func(raw json.RawMessage) error {
				var reply graphMessage
				if json.Unmarshal(raw, &reply) != nil {
					return ErrInvalid
				}
				reply.ReplyToID = m.ID
				return add(reply)
			})
			if err != nil {
				return messenger.Page{}, err
			}
		}
	}
	// Graph returns descending collections. Canon receives each page in creation
	// order, and advances coverage only once every page has been committed.
	sort.SliceStable(p.Messages, func(i, j int) bool { return p.Messages[i].At.Before(p.Messages[j].At) })
	c.Next = wire.Next
	p.HasMore = wire.Next != ""
	if !p.HasMore {
		c.Pages = 0
		c.Since = c.High
		p.Through = c.High
	}
	raw, _ := json.Marshal(c)
	p.Next = messenger.Cursor(raw)
	if p.HasMore && p.Next == position {
		return messenger.Page{}, ErrInvalid
	}
	return p, nil
}
func (a *Adapter) message(ctx context.Context, m graphMessage) (messenger.Message, error) {
	if m.Created.IsZero() {
		return messenger.Message{}, ErrInvalid
	}
	out := messenger.Message{ID: m.ID, At: m.Created, Edited: m.Modified, Text: m.Body.Content, ReplyTo: m.ReplyToID, Thread: m.ReplyToID, Route: messenger.RouteParticipant}
	if strings.EqualFold(m.Body.ContentType, "html") {
		out.Text = plainHTML(out.Text)
	}
	switch {
	case m.MessageType == "systemEventMessage":
		out.Route = messenger.RouteSystem
		out.Author = messenger.Author{ID: a.cfg.Tenant + ":system"}
	case m.From.Application != nil:
		out.Route = messenger.RouteAutomation
		out.Author = messenger.Author{ID: a.cfg.Tenant + ":app:" + m.From.Application.ID, Name: m.From.Application.DisplayName}
	case m.From.User != nil:
		user := m.From.User
		tid := user.TenantID
		if tid == "" {
			tid = a.cfg.Tenant
		}
		id := tid + ":" + user.ID
		if user.ID == "" {
			return messenger.Message{}, ErrInvalid
		}
		out.Author = messenger.Author{ID: id, Name: user.DisplayName}
		if cached, ok := a.profiles[id]; ok {
			out.Author = cached
		} else if tid == a.cfg.Tenant {
			var profile struct{ ID, DisplayName, Mail string }
			if err := a.get(ctx, graph+"/users/"+segment(user.ID)+"?$select=id,displayName,mail", &profile); err == ErrNotFound {
				profile.ID = user.ID
			} else if err != nil {
				return messenger.Message{}, err
			}
			if profile.ID != user.ID {
				return messenger.Message{}, ErrInvalid
			}
			out.Author.Email = profile.Mail
			if profile.DisplayName != "" {
				out.Author.Name = profile.DisplayName
			}
			a.profiles[id] = out.Author
		}
		if id == a.cfg.Account {
			out.Route = messenger.RoutePerson
		}
	default:
		out.Author = messenger.Author{ID: a.cfg.Tenant + ":unknown"}
	}
	for _, attachment := range m.Attachments {
		out.Attachments = append(out.Attachments, messenger.Attachment{ID: attachment.ID, Name: attachment.Name, MIMEType: attachment.ContentType})
	}
	return out, nil
}

// HTML is imported as text only; no link, image, script or attachment is fetched.
func plainHTML(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			out.WriteByte(' ')
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(out.String())), " ")
}
