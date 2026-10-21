-- SPDX-License-Identifier: Apache-2.0
-- Synthetic attribution scenarios, applied after state.sql.
INSERT INTO sessions(id,source,created_source,user_id,chat_id,chat_type,started_at,model_config) VALUES
 ('owner-chat','telegram','telegram','owner-user','owner-chat','private',200,'{}'),
 ('scheduled','cron','cron',NULL,NULL,NULL,200,'{}'),
 ('delegate','telegram','telegram','owner-user','owner-chat','private',200,'{"_delegate_from":"synthetic-parent"}'),
 ('other-agent','subagent','subagent',NULL,NULL,NULL,200,'{}'),
 ('other-chat','telegram','telegram','other-user','other-chat','private',200,'{}');
INSERT INTO messages(id,session_id,role,content,timestamp,message_uid) VALUES
 (10,'scheduled','user','I prefer brief replies.',200,'scheduled-uid'),
 (11,'owner-chat','user','[cron] I prefer brief replies.',201,'owner-uid'),
 (12,'delegate','user','I prefer brief replies.',202,'delegate-uid'),
 (13,'owner-chat','tool','I prefer brief replies.',203,'tool-uid'),
 (14,'owner-chat','system','I prefer brief replies.',204,'system-uid'),
 (15,'other-agent','user','I prefer brief replies.',205,'agent-uid'),
 (16,'owner-chat','assistant','I prefer brief replies.',206,'assistant-uid'),
 (17,'other-chat','user','I prefer brief replies.',207,'external-uid');
