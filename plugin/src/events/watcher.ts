import type { FileEventInput, FileEventKind } from "../grpc/client";

// Mirrors internal/scanner.DefaultIgnoreGlobs on the Go side: Obsidian's
// own config dir (holds this plugin's manifest.json/token.json), git
// metadata, and Obsidian's local trash must never be synced.
const IGNORED_PREFIXES = [".obsidian/", ".git/", ".trash/"];

export function isIgnoredPath(relPath: string): boolean {
	return IGNORED_PREFIXES.some((p) => relPath === p.slice(0, -1) || relPath.startsWith(p));
}

/**
 * Collapses rapid-fire vault events per path (a single user save often
 * fires more than one vault event for the same path in quick succession)
 * and flushes a debounced batch after a quiet period.
 */
export class SyncEventQueue {
	private pending = new Map<string, FileEventInput>();
	private timer: ReturnType<typeof setTimeout> | null = null;

	constructor(
		private readonly flushDelayMs: number,
		private readonly onFlush: (events: FileEventInput[]) => void,
	) {}

	enqueue(relativePath: string, kind: FileEventKind, oldRelativePath?: string): void {
		if (isIgnoredPath(relativePath)) return;

		this.pending.set(relativePath, {
			relative_path: relativePath,
			kind,
			old_relative_path: oldRelativePath,
		});

		if (this.timer) clearTimeout(this.timer);
		this.timer = setTimeout(() => this.flush(), this.flushDelayMs);
	}

	private flush(): void {
		this.timer = null;
		if (this.pending.size === 0) return;
		const events = Array.from(this.pending.values());
		this.pending.clear();
		this.onFlush(events);
	}

	/** Cancels any pending debounce timer without flushing -- used on
	 * plugin unload so a stray sync doesn't fire after the daemon has
	 * already been told to shut down. */
	cancel(): void {
		if (this.timer) {
			clearTimeout(this.timer);
			this.timer = null;
		}
		this.pending.clear();
	}
}
