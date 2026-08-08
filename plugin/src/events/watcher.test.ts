import * as assert from "node:assert";
import { test } from "node:test";

import { isIgnoredPath, SyncEventQueue } from "./watcher";

test("collapses rapid duplicate events for the same path", async () => {
	const flushes: unknown[][] = [];
	const q = new SyncEventQueue(20, (events) => flushes.push(events));

	q.enqueue("note.md", "MODIFY");
	q.enqueue("note.md", "MODIFY");
	q.enqueue("note.md", "MODIFY");

	await new Promise((r) => setTimeout(r, 60));

	assert.strictEqual(flushes.length, 1);
	assert.strictEqual(flushes[0].length, 1);
	assert.strictEqual((flushes[0][0] as { relative_path: string }).relative_path, "note.md");
});

test("keeps distinct paths separate in one flush", async () => {
	const flushes: unknown[][] = [];
	const q = new SyncEventQueue(20, (events) => flushes.push(events));

	q.enqueue("a.md", "MODIFY");
	q.enqueue("b.md", "CREATE");

	await new Promise((r) => setTimeout(r, 60));

	assert.strictEqual(flushes.length, 1);
	assert.strictEqual(flushes[0].length, 2);
});

test("resets the debounce timer on new activity for the same path", async () => {
	const flushes: unknown[][] = [];
	const q = new SyncEventQueue(50, (events) => flushes.push(events));

	q.enqueue("note.md", "MODIFY");
	await new Promise((r) => setTimeout(r, 30));
	q.enqueue("note.md", "MODIFY"); // should push the flush out again, not let it fire early
	await new Promise((r) => setTimeout(r, 30));

	assert.strictEqual(flushes.length, 0, "should not have flushed yet");

	await new Promise((r) => setTimeout(r, 40));
	assert.strictEqual(flushes.length, 1);
});

test("filters ignored paths (.obsidian, .git, .trash)", async () => {
	const flushes: unknown[][] = [];
	const q = new SyncEventQueue(20, (events) => flushes.push(events));

	q.enqueue(".obsidian/plugins/obsynk/token.json", "MODIFY");
	q.enqueue(".git/HEAD", "MODIFY");
	q.enqueue(".trash/note.md", "MODIFY");
	q.enqueue("real-note.md", "MODIFY");

	await new Promise((r) => setTimeout(r, 60));

	assert.strictEqual(flushes.length, 1);
	assert.strictEqual(flushes[0].length, 1);
	assert.strictEqual((flushes[0][0] as { relative_path: string }).relative_path, "real-note.md");
});

test("cancel() clears the pending timer without flushing", async () => {
	const flushes: unknown[][] = [];
	const q = new SyncEventQueue(20, (events) => flushes.push(events));

	q.enqueue("note.md", "MODIFY");
	q.cancel();

	await new Promise((r) => setTimeout(r, 60));
	assert.strictEqual(flushes.length, 0);
});

test("rename events carry the old relative path", async () => {
	const flushes: unknown[][] = [];
	const q = new SyncEventQueue(20, (events) => flushes.push(events));

	q.enqueue("new-name.md", "RENAME", "old-name.md");
	await new Promise((r) => setTimeout(r, 60));

	assert.strictEqual(flushes.length, 1);
	const ev = flushes[0][0] as { relative_path: string; old_relative_path?: string };
	assert.strictEqual(ev.relative_path, "new-name.md");
	assert.strictEqual(ev.old_relative_path, "old-name.md");
});

test("isIgnoredPath matches the exact dir and anything nested under it", () => {
	assert.strictEqual(isIgnoredPath(".obsidian"), true);
	assert.strictEqual(isIgnoredPath(".obsidian/manifest.json"), true);
	assert.strictEqual(isIgnoredPath(".git/HEAD"), true);
	assert.strictEqual(isIgnoredPath(".trash/x.md"), true);
	assert.strictEqual(isIgnoredPath("note.md"), false);
	assert.strictEqual(isIgnoredPath("not-obsidian/file.md"), false);
});
