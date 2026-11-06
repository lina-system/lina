// Inspect or settle permission dialogs on one retained session; never answer human questions.
import { homedir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import {
	isPermissionDialog,
	permissionDecision,
} from "./omo_permission_policy.mjs";

const [engine, ref, cwd, access, action] = process.argv.slice(2);
const permissions = access === "read-only"
	? { permissionPreset: "read-only" }
	: {};
const agentDir = process.env.OMO_CODING_AGENT_DIR ||
	join(homedir(), ".omo", "agent");
let client;
let reply;
try {
	if (
		!["read-only", "workspace-write", "full"].includes(access) ||
		!["inspect", "settle"].includes(action)
	) {
		throw new Error("invalid permission request");
	}
	const { RpcClient } = await import(
		pathToFileURL(join(engine, "dist/modes/rpc/rpc-client.js")).href
	);
	const { resolveSessionRef } = await import(
		pathToFileURL(join(engine, "dist/modes/rpc/host-session-ref.js")).href
	);
	client = new RpcClient({
		socketPath: process.env.SENPI_RPC_SOCKET ||
			join(agentDir, "rpc", "rpc.sock"),
		keyRoot: agentDir,
	});
	await client.start();
	const found = await resolveSessionRef(client, ref, agentDir);
	if (found.kind !== "live") throw new Error(`session is ${found.kind}`);
	await client.openSession({
		sessionPath: found.row.sessionPath,
		cwd: found.row.cwd,
		...permissions,
	});
	const state = await client.getState();
	const dialogs = (state.pendingDialogs || []).filter(isPermissionDialog);
	const decisions = [];
	if (action === "settle") {
		const messages = dialogs.some((dialog) =>
				dialog.title.startsWith("Permission required: eval")
			)
			? await client.getMessages()
			: [];
		for (const dialog of dialogs) {
			const call = messages.flatMap((message) =>
				Array.isArray(message.content) ? message.content : []
			)
				.find((part) =>
					part.type === "toolCall" && part.id === dialog.toolCallId
				);
			const decision = permissionDecision(dialog, access, cwd, call);
			await client.sendExtensionUIResponse({
				type: "extension_ui_response",
				id: `lina-permission-${dialog.id}`,
				uiRequestId: dialog.id,
				value: decision.value,
			});
			decisions.push({ ...decision, request: dialog.id });
		}
	}
	reply = {
		action: "permissions",
		pending: dialogs.length > 0,
		decisions,
		state,
	};
} catch (error) {
	reply = {
		action: "error",
		reason: String(error?.message ?? error).slice(0, 300),
	};
} finally {
	if (client) await client.stop();
}
process.stdout.write(JSON.stringify(reply) + "\n");
process.exit(reply.action === "error" ? 1 : 0);
