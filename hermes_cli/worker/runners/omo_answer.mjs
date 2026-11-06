// Answer what an omo session waits on a person for, over the device's shared daemon.
// `omo host session` has no answer verb, and a steer only queues behind a question. The engine settles a question
// with an `extension_ui_response` on an attached connection, in one of two shapes:
//   pendingQuestions  "question" frames (a client with the question capability): answered with a comment
//   pendingDialogs    the fallback, one "select" per question whose "Other" option opens an "input"
// Run on LINA's pinned Node with the pinned engine (lina#34).
// usage: node omo_answer.mjs <engine> <durable-session-id> <answer-file> <access>      prints one JSON line
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { isPermissionDialog } from "./omo_permission_policy.mjs";

const [engine, sessionId, answerFile, access] = process.argv.slice(2);
const permissions = access === "read-only"
	? { permissionPreset: "read-only" }
	: {};
const done = (value, code = 0) => {
	process.stdout.write(JSON.stringify(value) + "\n");
	process.exit(code);
};
if (
	!engine || !sessionId || !answerFile ||
	!["read-only", "workspace-write", "full"].includes(access)
) {
	done({ action: "error", reason: "usage" }, 2);
}
const text = readFileSync(answerFile, "utf8").trim();
if (!text) done({ action: "error", reason: "empty_answer" }, 2);

let RpcClient, resolveSessionRef;
try {
	({ RpcClient } = await import(
		pathToFileURL(join(engine, "dist/modes/rpc/rpc-client.js")).href
	));
	({ resolveSessionRef } = await import(
		pathToFileURL(join(engine, "dist/modes/rpc/host-session-ref.js")).href
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
const pause = Number(process.env.LINA_WORKER_ANSWER_PAUSE_MS ?? 1200);
const OTHER = /other|type an answer|직접 입력|기타/i;
let frames = 0;
const respond = (frame) =>
	client.sendExtensionUIResponse({
		type: "extension_ui_response",
		id: `lina-worker-answer-${Date.now()}-${++frames}`,
		...frame,
	});

async function answerDialog(dialog) {
	if (dialog.method === "select") {
		const options = (dialog.options || []).map(String);
		const value = options.find((o) =>
			o.trim().toLowerCase() === text.toLowerCase()
		) ?? options.find((o) =>
			OTHER.test(o)
		);
		if (value === undefined) {
			await respond({ uiRequestId: dialog.id, cancelled: true });
			return {
				uiRequestId: dialog.id,
				method: "select",
				cancelled: true,
			};
		}
		await respond({ uiRequestId: dialog.id, value });
		return {
			uiRequestId: dialog.id,
			method: "select",
			value: value.trim().toLowerCase() === text.toLowerCase()
				? "option"
				: "other",
		};
	}
	if (dialog.method === "confirm") {
		await respond({
			uiRequestId: dialog.id,
			confirmed: !/^(no|n|아니|안 ?돼)/i.test(text),
		});
		return { uiRequestId: dialog.id, method: "confirm" };
	}
	await respond({ uiRequestId: dialog.id, value: text });
	return { uiRequestId: dialog.id, method: dialog.method };
}

let reply;
try {
	await client.start();
	const found = await resolveSessionRef(client, sessionId, agentDir);
	if (found.kind !== "live") {
		done({
			action: "refuse",
			reason: found.kind === "unknown" ? "unknown_session" : "not_live",
			kind: found.kind,
		});
	}
	await client.openSession({
		sessionPath: found.row.sessionPath,
		cwd: found.row.cwd,
		...permissions,
	});
	const steps = [];
	// Several questions come one at a time, and a fallback select may open an input: at most 8 frames.
	for (let step = 0; step < 8; step++) {
		const state = await client.getState();
		if ((state.pendingDialogs || []).some(isPermissionDialog)) {
			reply = { action: "refuse", reason: "permission_dialog" };
			break;
		}
		const question = (state.pendingQuestions || []).find((q) => q && q.id);
		const dialog = (state.pendingDialogs || []).find((d) =>
			d && d.id &&
			["select", "input", "editor", "confirm"].includes(d.method)
		);
		if (question) {
			await respond({
				uiRequestId: question.id,
				answers: {},
				comment: text,
				answerId: `lina-worker-${question.id}`,
			});
			steps.push({
				uiRequestId: question.id,
				method: "question",
				requestId: question.requestId,
			});
		} else if (dialog) {
			steps.push(await answerDialog(dialog));
		} else {
			break;
		}
		await new Promise((resolve) => setTimeout(resolve, pause));
	}
	const after = await client.getState();
	const lifecycle = after.lifecycle || {};
	reply ??= {
		action: "answer",
		steps: steps.length,
		done: steps,
		still_pending: (after.pendingQuestions || []).length +
			(after.pendingDialogs || []).length,
		human: ((lifecycle.waitingOn || {}).human || []).length,
		lifecycle: lifecycle.state,
		streaming: after.isStreaming,
	};
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
