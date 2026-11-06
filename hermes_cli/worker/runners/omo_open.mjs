// Open or reattach an omo session with per-session permissions through LINA's pinned engine.
// usage: node omo_open.mjs <engine> <cwd> <name> <prompt-file> <provider> <model-id> <thinking> <access> [ref]
// Prints one JSON line shaped like `omo host session open --json`, or an attach receipt.
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [
	engine,
	cwd,
	name,
	promptFile,
	provider,
	modelId,
	thinking,
	access,
	ref,
] = process.argv.slice(2);
const presets = new Map([["read-only", "read-only"], [
	"workspace-write",
	"workspace",
], ["full", "full-access"]]);
const permissionPreset = presets.get(access);
const done = (value, code = 0) => {
	process.stdout.write(JSON.stringify(value) + "\n");
	process.exit(code);
};
if (
	!engine || !cwd || !name || (!promptFile && !ref) || !permissionPreset ||
	Boolean(provider) !== Boolean(modelId)
) {
	done({ action: "error", reason: "usage" }, 2);
}

let RpcClient;
try {
	({ RpcClient } = await import(
		pathToFileURL(join(engine, "dist/modes/rpc/rpc-client.js")).href
	));
} catch (error) {
	done({
		action: "error",
		reason: "engine_not_found",
		detail: String(error).slice(0, 200),
	}, 1);
}
const agentDir = process.env.OMO_CODING_AGENT_DIR ||
	join(homedir(), ".omo", "agent");
const client = new RpcClient({
	socketPath: process.env.SENPI_RPC_SOCKET ||
		join(agentDir, "rpc", "rpc.sock"),
	keyRoot: agentDir,
});

let reply;
try {
	await client.start();
	if (ref) {
		const { resolveSessionRef } = await import(
			pathToFileURL(join(engine, "dist/modes/rpc/host-session-ref.js"))
				.href
		);
		const found = await resolveSessionRef(client, ref, agentDir);
		if (found.kind !== "live") throw new Error(`session is ${found.kind}`);
		await client.openSession({
			sessionPath: found.row.sessionPath,
			cwd: found.row.cwd,
			permissionPreset,
		});
		reply = { action: "attach" };
	} else {
		// The session outlives this short client, as with `omo host session open`.
		const opened = await client.openSession({
			cwd,
			retain_on_disconnect: true,
			permissionPreset,
			...(provider ? { provider, modelId } : {}),
			...(thinking ? { thinkingLevel: thinking } : {}),
		});
		const { state } = opened;
		await client.setSessionName(name);
		const disposition = await client.prompt(
			readFileSync(promptFile, "utf8") +
				`\n\nWorker access mode: ${access}. Permission dialogs are answered by the worker, never by a person. ` +
				`A permission denial is final for this run: do not ask for approval, retry the denied operation, or use another tool to bypass it. ` +
				`If access blocks the requested task, report that and finish. ` +
				(access !== "full"
					? `Use file tools directly. When bash is hidden, the worker accepts a single literal eval bridge, for example await tool.bash({"command":"ls"}). Use JSON arguments and no surrounding code. Workspace bridges support ls, git status --short, git add with a local filename, and git commit -m with a literal message, joined by && if needed. General eval code is denied. `
					: ""),
		);
		reply = {
			action: "open",
			sessionId: opened.sessionId,
			sessionPath: state.sessionFile,
			durableSessionId: state.sessionId,
			cwd: state.cwd,
			name,
			prompt: { disposition },
			model: state.model
				? { provider: state.model.provider, id: state.model.id }
				: null,
			thinking: state.thinkingLevel,
		};
	}
} catch (error) {
	reply = {
		action: "error",
		reason: "rpc",
		detail: String(error?.message ?? error).slice(0, 300),
	};
} finally {
	await client.stop().catch(() => {});
}
done(reply, reply.action === "error" ? 1 : 0);
