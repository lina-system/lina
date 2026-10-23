-- SPDX-License-Identifier: Apache-2.0
UPDATE sessions SET end_reason='compression' WHERE id='synthetic-session';
INSERT INTO sessions(id,source,started_at,parent_session_id,model_config) VALUES
    ('resumed','cli',105,'synthetic-session','{}'),
    ('fork','cli',110,'synthetic-session','{"_branched_from":"synthetic-session"}'),
    ('reset','cli',115,'synthetic-session','{"_reset_from":"synthetic-session"}');
INSERT INTO messages(id,session_id,role,content,timestamp,message_uid) VALUES
    (3,'synthetic-session','user','Synthetic request',100,'synthetic-message-1'),
    (10,'resumed','assistant','Synthetic response',101,'synthetic-message-2'),
    (11,'fork','assistant','Fork response',110,'synthetic-message-2'),
    (12,'reset','assistant','Reset response',115,'synthetic-message-2');
