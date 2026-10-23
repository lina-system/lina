-- SPDX-License-Identifier: Apache-2.0
-- Apply after the base fixture has canonical bindings for messages 1 and 2.
-- Two home tags share a root and UID, even pointing to the same stored message.
INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version)
    VALUES (1,'synthetic-message-1','other-home:synthetic-session','canonical',1);
-- A fork can bind the same UID independently to its own message.
INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp)
    VALUES (9,'fork','cli','fork-conversation','assistant','Fork response',110);
INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version)
    VALUES (9,'synthetic-message-2','home:fork','canonical',1);
