-- SPDX-License-Identifier: Apache-2.0
-- Add to lcm.sql. Candidate UID deliberately names the other host message.
INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version,proof_kind)
 VALUES(1,'synthetic-message-2','home:synthetic-session','alias_candidate',1,'position_proof');
INSERT INTO message_relations(relation_id,store_id,kind,related_store_id,ordinal,observed_at,created_at)
 VALUES(2,1,'composite',1,0,100,104),(3,1,'composite',2,1,100,104);
