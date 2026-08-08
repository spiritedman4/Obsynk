package grpcserver

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	obsynkv1 "obsynk/gen/go/obsynk/v1"
	"obsynk/internal/config"
	"obsynk/internal/drive"
	"obsynk/internal/statestore"
	"obsynk/internal/syncengine"
)

// Server implements obsynkv1.ObsynkServiceServer for one vault. Drive
// connectivity is optional at construction time -- GetStatus/StartLogin
// work before the user has connected Drive; SyncPaths/FullSync return
// FailedPrecondition until they have.
type Server struct {
	obsynkv1.UnimplementedObsynkServiceServer

	version   string
	vaultPath string

	configPath   string
	tokenPath    string
	manifestPath string

	mu             sync.Mutex
	cfg            config.Config
	store          *statestore.Store
	driveClient    *drive.Client
	engine         *syncengine.Engine
	connectedEmail string
	lastSyncUnixMs int64

	stopCh chan struct{}
}

// NewServer loads this vault's persisted config and sync manifest, and --
// best-effort -- reconnects to Drive if credentials and a cached token are
// already present (e.g. this is a daemon restart, not the first run).
func NewServer(version, vaultPath string) (*Server, error) {
	dataDir := filepath.Join(vaultPath, ".obsidian", "plugins", "obsynk")

	s := &Server{
		version:      version,
		vaultPath:    vaultPath,
		configPath:   filepath.Join(dataDir, "config.json"),
		tokenPath:    filepath.Join(dataDir, "token.json"),
		manifestPath: filepath.Join(dataDir, "sync-state.json"),
		stopCh:       make(chan struct{}),
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		return nil, fmt.Errorf("grpcserver: loading config: %w", err)
	}
	s.cfg = cfg

	store, err := statestore.Open(s.manifestPath, vaultPath)
	if err != nil {
		return nil, fmt.Errorf("grpcserver: opening statestore: %w", err)
	}
	s.store = store

	if cfg.ClientID != "" && cfg.ClientSecret != "" {
		if _, err := os.Stat(s.tokenPath); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := s.initDrive(ctx); err != nil {
				log.Printf("obsynkd: reconnecting to drive on startup: %v", err)
			}
		}
	}

	return s, nil
}

// StopRequested is closed once a client has called Shutdown, signaling main
// to begin a graceful stop.
func (s *Server) StopRequested() <-chan struct{} {
	return s.stopCh
}

// initDrive builds the Drive client from cached/just-obtained credentials,
// ensures the vault's root Drive folder exists, and wires up the sync
// engine. Called both at startup (if a token is already cached) and right
// after a successful StartLogin.
func (s *Server) initDrive(ctx context.Context) error {
	s.mu.Lock()
	creds := drive.Credentials{ClientID: s.cfg.ClientID, ClientSecret: s.cfg.ClientSecret}
	rootName := s.cfg.DriveRootFolderName
	rootID := s.cfg.DriveRootFolderID
	s.mu.Unlock()

	cache := drive.FileTokenCache{Path: s.tokenPath}
	client, err := drive.NewClient(ctx, creds, cache)
	if err != nil {
		return fmt.Errorf("connecting to drive: %w", err)
	}

	if rootName == "" {
		rootName = "Obsidian Vault - " + filepath.Base(s.vaultPath)
	}
	if rootID == "" {
		id, err := client.EnsureRootFolder(ctx, rootName, "")
		if err != nil {
			return fmt.Errorf("ensuring drive root folder: %w", err)
		}
		rootID = id
	}

	engine := &syncengine.Engine{
		VaultRoot:   s.vaultPath,
		DriveRootID: rootID,
		Drive:       client,
		Store:       s.store,
		Workers:     4,
	}

	var email string
	if about, err := client.AboutMe(ctx); err == nil && about.User != nil {
		email = about.User.EmailAddress
	}

	s.mu.Lock()
	s.cfg.DriveRootFolderName = rootName
	s.cfg.DriveRootFolderID = rootID
	s.driveClient = client
	s.engine = engine
	s.connectedEmail = email
	cfgToSave := s.cfg
	s.mu.Unlock()

	if err := cfgToSave.Save(s.configPath); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	return nil
}

func (s *Server) GetStatus(ctx context.Context, req *obsynkv1.GetStatusRequest) (*obsynkv1.StatusResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return &obsynkv1.StatusResponse{
		DriveConnected:    s.driveClient != nil,
		DriveAccountEmail: s.connectedEmail,
		LastSyncUnixMs:    s.lastSyncUnixMs,
		PendingOperations: 0,
		DaemonVersion:     s.version,
	}, nil
}

func (s *Server) StartLogin(req *obsynkv1.StartLoginRequest, stream grpc.ServerStreamingServer[obsynkv1.LoginEvent]) error {
	clientID := req.GetClientId()
	clientSecret := req.GetClientSecret()

	s.mu.Lock()
	if clientID == "" {
		clientID = s.cfg.ClientID
	}
	if clientSecret == "" {
		clientSecret = s.cfg.ClientSecret
	}
	s.mu.Unlock()

	if clientID == "" || clientSecret == "" {
		return status.Error(codes.InvalidArgument, "client_id and client_secret are required")
	}

	creds := drive.Credentials{ClientID: clientID, ClientSecret: clientSecret}
	cache := drive.FileTokenCache{Path: s.tokenPath}
	ctx := stream.Context()

	err := drive.LoginInteractive(ctx, creds, cache, func(url string) {
		_ = stream.Send(&obsynkv1.LoginEvent{Event: &obsynkv1.LoginEvent_AuthorizeUrl{AuthorizeUrl: url}})
	})
	if err != nil {
		return stream.Send(&obsynkv1.LoginEvent{Event: &obsynkv1.LoginEvent_Error{Error: err.Error()}})
	}

	s.mu.Lock()
	s.cfg.ClientID = clientID
	s.cfg.ClientSecret = clientSecret
	s.mu.Unlock()

	if err := s.initDrive(ctx); err != nil {
		return stream.Send(&obsynkv1.LoginEvent{Event: &obsynkv1.LoginEvent_Error{Error: err.Error()}})
	}

	return stream.Send(&obsynkv1.LoginEvent{Event: &obsynkv1.LoginEvent_Connected{Connected: true}})
}

func (s *Server) GetConfig(ctx context.Context, req *obsynkv1.GetConfigRequest) (*obsynkv1.ConfigResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return &obsynkv1.ConfigResponse{
		ClientId:            s.cfg.ClientID,
		HasClientSecret:     s.cfg.ClientSecret != "",
		DriveRootFolderName: s.cfg.DriveRootFolderName,
		DriveRootFolderId:   s.cfg.DriveRootFolderID,
	}, nil
}

func (s *Server) SetConfig(ctx context.Context, req *obsynkv1.SetConfigRequest) (*obsynkv1.ConfigResponse, error) {
	s.mu.Lock()
	if req.GetClientId() != "" {
		s.cfg.ClientID = req.GetClientId()
	}
	if req.GetClientSecret() != "" {
		s.cfg.ClientSecret = req.GetClientSecret()
	}
	if req.GetDriveRootFolderName() != "" {
		s.cfg.DriveRootFolderName = req.GetDriveRootFolderName()
	}
	cfg := s.cfg
	s.mu.Unlock()

	if err := cfg.Save(s.configPath); err != nil {
		return nil, status.Errorf(codes.Internal, "saving config: %v", err)
	}

	return s.GetConfig(ctx, &obsynkv1.GetConfigRequest{})
}

func (s *Server) SyncPaths(req *obsynkv1.SyncPathsRequest, stream grpc.ServerStreamingServer[obsynkv1.SyncProgress]) error {
	engine := s.currentEngine()
	if engine == nil {
		return status.Error(codes.FailedPrecondition, "drive is not connected yet")
	}

	events := make([]syncengine.FileEvent, 0, len(req.GetEvents()))
	for _, e := range req.GetEvents() {
		events = append(events, syncengine.FileEvent{
			RelativePath:    e.GetRelativePath(),
			Kind:            convertEventKind(e.GetKind()),
			OldRelativePath: e.GetOldRelativePath(),
		})
	}

	results, total, err := engine.SyncPaths(stream.Context(), events)
	if err != nil {
		return status.Errorf(codes.Internal, "sync failed: %v", err)
	}
	return s.streamResults(stream, results, total)
}

func (s *Server) FullSync(req *obsynkv1.FullSyncRequest, stream grpc.ServerStreamingServer[obsynkv1.SyncProgress]) error {
	engine := s.currentEngine()
	if engine == nil {
		return status.Error(codes.FailedPrecondition, "drive is not connected yet")
	}

	results, total, err := engine.FullSync(stream.Context())
	if err != nil {
		return status.Errorf(codes.Internal, "full sync failed: %v", err)
	}
	return s.streamResults(stream, results, total)
}

func (s *Server) currentEngine() *syncengine.Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.engine
}

func (s *Server) streamResults(stream grpc.ServerStreamingServer[obsynkv1.SyncProgress], results <-chan syncengine.OpResult, total int) error {
	for r := range results {
		msg := &obsynkv1.SyncProgress{
			RelativePath:    r.Op.RelativePath,
			Op:              convertOp(r.Op.Kind),
			Status:          obsynkv1.SyncProgress_SUCCEEDED,
			TotalOperations: int32(total),
		}
		if r.Err != nil {
			msg.Status = obsynkv1.SyncProgress_FAILED
			msg.ErrorMessage = r.Err.Error()
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}

	if err := s.store.Save(); err != nil {
		return status.Errorf(codes.Internal, "saving sync state: %v", err)
	}

	s.mu.Lock()
	s.lastSyncUnixMs = time.Now().UnixMilli()
	s.mu.Unlock()

	return nil
}

func (s *Server) Shutdown(ctx context.Context, req *obsynkv1.ShutdownRequest) (*obsynkv1.ShutdownResponse, error) {
	s.mu.Lock()
	select {
	case <-s.stopCh:
		// already requested
	default:
		close(s.stopCh)
	}
	s.mu.Unlock()
	return &obsynkv1.ShutdownResponse{}, nil
}

func convertEventKind(k obsynkv1.FileEvent_Kind) syncengine.FileEventKind {
	switch k {
	case obsynkv1.FileEvent_CREATE:
		return syncengine.EventCreate
	case obsynkv1.FileEvent_DELETE:
		return syncengine.EventDelete
	case obsynkv1.FileEvent_RENAME:
		return syncengine.EventRename
	default:
		return syncengine.EventModify
	}
}

func convertOp(d syncengine.Decision) obsynkv1.SyncProgress_Op {
	switch d {
	case syncengine.DecisionPush, syncengine.DecisionCreateRemote:
		return obsynkv1.SyncProgress_PUSH
	case syncengine.DecisionPull, syncengine.DecisionCreateLocal:
		return obsynkv1.SyncProgress_PULL
	case syncengine.DecisionPushDelete:
		return obsynkv1.SyncProgress_PUSH_DELETE
	case syncengine.DecisionPullDelete:
		return obsynkv1.SyncProgress_PULL_DELETE
	case syncengine.DecisionConflict:
		return obsynkv1.SyncProgress_CONFLICT_RESOLVED
	default:
		return obsynkv1.SyncProgress_SKIPPED
	}
}
