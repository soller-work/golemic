import type { AssistantMessage } from "@earendil-works/pi-ai";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";
import { basename } from "node:path";

function formatK(tokens: number): string {
	const k = tokens / 1000;
	if (k < 10) return `${k.toFixed(1)}k`;
	return `${Math.round(k)}k`;
}

export default function (pi: ExtensionAPI) {
	pi.on("session_start", (_event, ctx) => {
		if (ctx.mode !== "tui") return;

		ctx.ui.setFooter((tui, theme, footerData) => {
			const unsubscribeBranch = footerData.onBranchChange(() => tui.requestRender());

			return {
				dispose: unsubscribeBranch,
				invalidate() {},
				render(width: number): string[] {
					let input = 0;
					let output = 0;
					let cacheRead = 0;
					let cacheWrite = 0;
					let cost = 0;

					for (const entry of ctx.sessionManager.getBranch()) {
						if (entry.type !== "message" || entry.message.role !== "assistant") continue;
						const message = entry.message as AssistantMessage;
						input += message.usage?.input ?? 0;
						output += message.usage?.output ?? 0;
						cacheRead += message.usage?.cacheRead ?? 0;
						cacheWrite += message.usage?.cacheWrite ?? 0;
						cost += message.usage?.cost?.total ?? 0;
					}

					const usage = ctx.getContextUsage();
					const contextTokens = usage?.tokens ?? 0;
					const contextLabel = `CTX ${formatK(contextTokens)}`;
					const contextColor =
						contextTokens < 80_000 ? "success" : contextTokens <= 100_000 ? "warning" : "error";
					const context = theme.fg(contextColor, contextLabel);

					const fmt = (n: number) => formatK(n);
					const cwd = basename(ctx.cwd) || ctx.cwd;
					const branch = footerData.getGitBranch();
					const git = branch ? ` ${branch}` : "";
					const left = theme.fg("dim", `${cwd}${git}  ↑${fmt(input)} ↓${fmt(output)} R${fmt(cacheRead)} W${fmt(cacheWrite)} $${cost.toFixed(3)}`);
					const right = `${context} ${theme.fg("dim", ctx.model?.id ?? "no-model")}`;

					const pad = " ".repeat(Math.max(1, width - visibleWidth(left) - visibleWidth(right)));
					return [truncateToWidth(left + pad + right, width)];
				},
			};
		});
	});
}
