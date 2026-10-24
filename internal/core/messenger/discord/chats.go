// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"fmt"
	"net/url"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// chatInfo keeps what a chat needs beyond the registry row: the guild its
// members come from.
type chatInfo struct {
	guild string
}

// Discord channel types that carry messages. Categories, voice and stage
// channels do not become source chats; a forum's posts are its threads.
const (
	typeText        = 0
	typeAnnounce    = 5
	typeThreadNews  = 10
	typeThreadPub   = 11
	typeThreadPriv  = 12
	typeForum       = 15
	pageSize        = 100
	listsGuildLimit = 200
)

type guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type channel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	ParentID string `json:"parent_id"`
}

type threads struct {
	Threads []channel `json:"threads"`
	HasMore bool      `json:"has_more"`
}

// ListChats lists the guilds the bot belongs to, each guild's text and
// announcement channels, its active threads and their archived public threads.
func (a *Adapter) ListChats(ctx context.Context) ([]messenger.Chat, error) {
	guilds, err := a.guilds(ctx)
	if err != nil {
		return nil, err
	}
	var chats []messenger.Chat
	found := map[string]chatInfo{}
	for _, g := range guilds {
		var channels []channel
		if err = a.client.Get(ctx, "/guilds/"+g.ID+"/channels", nil, &channels); err != nil {
			return nil, err
		}
		var active threads
		if err = a.client.Get(ctx, "/guilds/"+g.ID+"/threads/active", nil, &active); err != nil {
			return nil, err
		}
		var parents []string
		for _, c := range channels {
			switch c.Type {
			case typeText, typeAnnounce:
				chats = append(chats, messenger.Chat{ID: c.ID, Name: c.Name, Kind: "channel"})
				found[c.ID] = chatInfo{guild: g.ID}
				parents = append(parents, c.ID)
			case typeForum:
				parents = append(parents, c.ID)
			}
		}
		for _, t := range active.Threads {
			if !threadType(t.Type) {
				continue
			}
			chats = append(chats, messenger.Chat{ID: t.ID, Name: t.Name, Kind: "thread"})
			found[t.ID] = chatInfo{guild: g.ID}
		}
		for _, parent := range parents {
			archived, err := a.archived(ctx, parent)
			if err != nil {
				return nil, err
			}
			for _, t := range archived {
				if !threadType(t.Type) {
					continue
				}
				chats = append(chats, messenger.Chat{ID: t.ID, Name: t.Name, Kind: "thread"})
				found[t.ID] = chatInfo{guild: g.ID}
			}
		}
	}
	a.mu.Lock()
	a.chats = found
	a.loaded = true
	a.mu.Unlock()
	return chats, nil
}

func threadType(t int) bool {
	return t == typeThreadNews || t == typeThreadPub || t == typeThreadPriv
}

func (a *Adapter) guilds(ctx context.Context) ([]guild, error) {
	var all []guild
	query := url.Values{"limit": {fmt.Sprint(listsGuildLimit)}}
	for {
		var page []guild
		if err := a.client.Get(ctx, "/users/@me/guilds", query, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < listsGuildLimit {
			return all, nil
		}
		query.Set("after", page[len(page)-1].ID)
	}
}

// archived pages one parent's archived public threads until the platform says
// it holds no more.
func (a *Adapter) archived(ctx context.Context, parent string) ([]channel, error) {
	var all []channel
	query := url.Values{"limit": {fmt.Sprint(pageSize)}}
	for {
		var page threads
		if err := a.client.Get(ctx, "/channels/"+parent+"/threads/archived/public", query, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Threads...)
		if !page.HasMore || len(page.Threads) == 0 {
			return all, nil
		}
		query.Set("before", page.Threads[len(page.Threads)-1].ID)
	}
}

// Members lists the guild's members, paged by user id.
func (a *Adapter) Members(ctx context.Context, chat string) ([]messenger.Author, error) {
	info, err := a.chat(ctx, chat)
	if err != nil {
		return nil, err
	}
	if info.guild == "" {
		return nil, messenger.ErrInvalid
	}
	var all []messenger.Author
	query := url.Values{"limit": {"1000"}}
	for {
		var page []struct {
			User struct {
				ID       string `json:"id"`
				Username string `json:"username"`
				Global   string `json:"global_name"`
				Bot      bool   `json:"bot"`
			} `json:"user"`
		}
		if err := a.client.Get(ctx, "/guilds/"+info.guild+"/members", query, &page); err != nil {
			return nil, err
		}
		for _, m := range page {
			name := m.User.Global
			if name == "" {
				name = m.User.Username
			}
			all = append(all, messenger.Author{ID: m.User.ID, Name: name})
		}
		if len(page) < 1000 {
			return all, nil
		}
		query.Set("after", page[len(page)-1].User.ID)
	}
}

// ensureChats loads the listing once, so live events are filtered to chats this
// source reads.
func (a *Adapter) ensureChats(ctx context.Context) error {
	a.mu.Lock()
	loaded := a.loaded
	a.mu.Unlock()
	if loaded {
		return nil
	}
	_, err := a.ListChats(ctx)
	return err
}

// chat returns the listing of one chat, loading the listing when the source has
// not listed chats yet.
func (a *Adapter) chat(ctx context.Context, chat string) (chatInfo, error) {
	a.mu.Lock()
	info, known := a.chats[chat]
	a.mu.Unlock()
	if known {
		return info, nil
	}
	if _, err := a.ListChats(ctx); err != nil {
		return chatInfo{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	info, known = a.chats[chat]
	if !known {
		return chatInfo{}, messenger.ErrInvalid
	}
	return info, nil
}

// knownChat reports whether an event's channel is one this source reads.
func (a *Adapter) knownChat(chat string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.chats[chat]
	return ok
}
