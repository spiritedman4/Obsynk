import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";
import * as fs from "fs";
import * as path from "path";

// Loaded dynamically via @grpc/proto-loader rather than codegen'd (e.g.
// ts-proto): proto-loader gives full runtime functionality with far less
// build tooling, at the cost of compile-time message-shape checking, which
// the hand-written interfaces below approximate. Reasonable trade for a
// personal-use project; revisit with codegen if this is ever published.
//
// Loaded lazily (on first ObsynkClient construction) rather than at module
// top-level: if this ever throws, we want it to happen inside onload()'s
// try/catch (surfaced as a Notice, plugin still loads) rather than during
// module evaluation itself, which would make the *entire* plugin fail to
// load with just Obsidian's generic "failed to load plugin" toast and no
// actionable error.
//
// pluginDir is passed in explicitly (from main.ts, via
// FileSystemAdapter.getBasePath()) rather than derived from __dirname:
// Obsidian doesn't load plugins via plain Node require(), so __dirname
// inside the bundled main.js resolves to Obsidian's own app bundle
// directory (electron.asar\renderer), not the plugin's installed folder.
// Confirmed the hard way -- don't reintroduce __dirname here.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
let obsynkProto: any = null;
let obsynkProtoDir: string | null = null;

function getObsynkProto(pluginDir: string) {
	if (obsynkProto && obsynkProtoDir === pluginDir) return obsynkProto;

	const protoPath = path.join(pluginDir, "obsynk.proto");
	if (!fs.existsSync(protoPath)) {
		throw new Error(
			`Obsynk: obsynk.proto not found at ${protoPath}. It must sit directly alongside main.js in the plugin's installed folder.`,
		);
	}

	const packageDef = protoLoader.loadSync(protoPath, {
		keepCase: true,
		longs: String,
		enums: String,
		defaults: true,
		oneofs: true,
	});
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	obsynkProto = (grpc.loadPackageDefinition(packageDef) as any).obsynk.v1;
	obsynkProtoDir = pluginDir;
	return obsynkProto;
}

export interface StatusResponse {
	drive_connected: boolean;
	drive_account_email: string;
	last_sync_unix_ms: string;
	pending_operations: number;
	daemon_version: string;
}

export interface ConfigResponse {
	client_id: string;
	has_client_secret: boolean;
	drive_root_folder_name: string;
	drive_root_folder_id: string;
}

export interface SetConfigRequest {
	client_id?: string;
	client_secret?: string;
	drive_root_folder_name?: string;
}

export interface SyncProgressEvent {
	relative_path: string;
	op: string;
	status: string;
	error_message: string;
	// Total operations in the current sync batch, repeated on every message
	// so the client can show progress/percentage without a separate
	// "batch started" event.
	total_operations: number;
}

export type LoginEvent =
	| { authorize_url: string }
	| { connected: boolean }
	| { error: string };

export type FileEventKind = "MODIFY" | "CREATE" | "DELETE" | "RENAME";

export interface FileEventInput {
	relative_path: string;
	kind: FileEventKind;
	old_relative_path?: string;
}

export class ObsynkClient {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	private client: any;

	constructor(target: string, pluginDir: string) {
		this.client = new (getObsynkProto(pluginDir).ObsynkService)(target, grpc.credentials.createInsecure());
	}

	waitForReady(timeoutMs: number): Promise<void> {
		const deadline = new Date(Date.now() + timeoutMs);
		return new Promise((resolve, reject) => {
			this.client.waitForReady(deadline, (err?: Error) => (err ? reject(err) : resolve()));
		});
	}

	getStatus(): Promise<StatusResponse> {
		return new Promise((resolve, reject) => {
			this.client.GetStatus({}, (err: Error, resp: StatusResponse) => (err ? reject(err) : resolve(resp)));
		});
	}

	getConfig(): Promise<ConfigResponse> {
		return new Promise((resolve, reject) => {
			this.client.GetConfig({}, (err: Error, resp: ConfigResponse) => (err ? reject(err) : resolve(resp)));
		});
	}

	setConfig(req: SetConfigRequest): Promise<ConfigResponse> {
		return new Promise((resolve, reject) => {
			this.client.SetConfig(req, (err: Error, resp: ConfigResponse) => (err ? reject(err) : resolve(resp)));
		});
	}

	shutdown(): Promise<void> {
		return new Promise((resolve, reject) => {
			this.client.Shutdown({}, (err: Error) => (err ? reject(err) : resolve()));
		});
	}

	startLogin(clientId: string, clientSecret: string, onEvent: (ev: LoginEvent) => void): Promise<void> {
		return new Promise((resolve, reject) => {
			const call = this.client.StartLogin({ client_id: clientId, client_secret: clientSecret });
			call.on("data", (ev: LoginEvent) => onEvent(ev));
			call.on("end", () => resolve());
			call.on("error", (err: Error) => reject(err));
		});
	}

	syncPaths(events: FileEventInput[], onProgress: (p: SyncProgressEvent) => void): Promise<void> {
		return new Promise((resolve, reject) => {
			const call = this.client.SyncPaths({ events });
			call.on("data", (p: SyncProgressEvent) => onProgress(p));
			call.on("end", () => resolve());
			call.on("error", (err: Error) => reject(err));
		});
	}

	fullSync(onProgress: (p: SyncProgressEvent) => void): Promise<void> {
		return new Promise((resolve, reject) => {
			const call = this.client.FullSync({});
			call.on("data", (p: SyncProgressEvent) => onProgress(p));
			call.on("end", () => resolve());
			call.on("error", (err: Error) => reject(err));
		});
	}

	close(): void {
		this.client.close();
	}
}
