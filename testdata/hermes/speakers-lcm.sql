-- SPDX-License-Identifier: Apache-2.0
-- Synthetic attribution scenarios, applied after lcm.sql.
INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp) VALUES
 (10,'scheduled','cron','scheduled','user','I prefer brief replies.',200),
 (11,'owner-chat','telegram','owner-chat','user','[cron] I prefer brief replies.',201),
 (18,'owner-chat','cron','owner-chat','user','I prefer brief replies.',208),
 (19,'owner-chat','telegram','owner-chat','user','I prefer brief replies.',209),
 (20,'owner-chat','unknown-source','owner-chat','user','I prefer brief replies.',210);
