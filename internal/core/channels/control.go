// SPDX-License-Identifier: Apache-2.0

package channels

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
)

type Connection struct {
	Bot
	Sender, PrivateChat string
	Bindings            []Binding
}

// Connections returns only the authenticated owner's metadata, never credentials.
func (s *Store) Connections(ctx context.Context, person string) ([]Connection, error) {
	result := []Connection{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT bot,sender,private_chat FROM channel_connections WHERE person=? ORDER BY bot", person)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key string
			var c Connection
			if err = rows.Scan(&key, &c.Sender, &c.PrivateChat); err != nil {
				rows.Close()
				return err
			}
			var parts [2]string
			if err = json.Unmarshal([]byte(key), &parts); err != nil {
				rows.Close()
				return err
			}
			c.Bot = Bot{Platform: parts[0], Account: parts[1]}
			result = append(result, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i := range result {
			c := &result[i]
			bindings, err := tx.QueryContext(ctx, `SELECT c.chat,c.topic,a.agent_id,c.conversation,c.scope,c.unreachable FROM channel_bindings c JOIN agent_bindings a ON a.channel=c.address WHERE c.bot=? AND a.person_id=? ORDER BY c.chat,c.topic`, c.Bot.Key(), person)
			if err != nil {
				return err
			}
			for bindings.Next() {
				b := Binding{Address: Address{Bot: c.Bot}, Person: person}
				var raw []byte
				if err = bindings.Scan(&b.Chat, &b.Topic, &b.Agent, &b.Conversation, &raw, &b.Unreachable); err != nil {
					bindings.Close()
					return err
				}
				if err = json.Unmarshal(raw, &b.Scope); err != nil {
					bindings.Close()
					return err
				}
				c.Bindings = append(c.Bindings, b)
			}
			err = bindings.Err()
			bindings.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// Disconnect revokes dispatch and pairing while retaining ownership and delivery
// receipts. Reconnection cannot transfer the owner or replay old platform input.
func (s *Store) Disconnect(ctx context.Context, b Bot, person string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := owner(ctx, tx, b, person); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM agent_bindings WHERE channel IN (SELECT address FROM channel_bindings WHERE bot=?)", b.Key()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE channel_outbound SET state='blocked' WHERE bot=? AND state IN ('queued','refused')", b.Key()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET sender='',private_chat='',pairing_hash=NULL,pairing_expires=0 WHERE bot=?", b.Key())
		return err
	})
}
