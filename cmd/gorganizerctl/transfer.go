package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// dialDaemon connects to the running daemon's Unix socket.
func dialDaemon(socketPath string) (*grpc.ClientConn, error) {
	if socketPath == "" {
		socketPath = config.SocketPath()
	}
	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to daemon at %s: %w", socketPath, err)
	}
	return conn, nil
}

// runExport implements `gorganizerctl export`.
func runExport(args []string) int {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	gameID := fs.String("game", "", "internal game id (e.g. falloutnv, skyrimse)")
	out := fs.String("out", "", "output archive path (tar+zstd)")
	mods := fs.String("mods", "", "comma-separated mod folders (default: all)")
	profiles := fs.String("profiles", "", "comma-separated profile names (default: all)")
	noOverwrite := fs.Bool("no-overwrite", false, "exclude the Overwrite layer")
	noGameSettings := fs.Bool("no-game-settings", false, "exclude per-game settings")
	socketPath := fs.String("socket-path", "",
		"path to the daemon socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gameID == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "error: --game and --out are required")
		fs.Usage()
		return 2
	}
	outPath, err := filepath.Abs(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	conn, err := dialDaemon(*socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer conn.Close()

	client := pb.NewGorganizerClient(conn)
	stream, err := client.ExportInstance(context.Background(), &pb.ExportInstanceRequest{
		GameId:              *gameID,
		OutputPath:          outPath,
		ModFolders:          splitList(*mods),
		ProfileNames:        splitList(*profiles),
		IncludeOverwrite:    !*noOverwrite,
		IncludeGameSettings: !*noGameSettings,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: ExportInstance RPC: %v\n", err)
		return 1
	}
	return drainTransferStream(stream, "export")
}

// runImport implements `gorganizerctl import`.
func runImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	gameID := fs.String("game", "", "internal game id (e.g. falloutnv, skyrimse)")
	archive := fs.String("archive", "", "archive path to import")
	policy := fs.String("policy", "abort", "collision policy: abort|skip|rename|overwrite")
	dryRun := fs.Bool("dry-run", false, "preview the archive manifest and collisions without importing")
	socketPath := fs.String("socket-path", "",
		"path to the daemon socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gameID == "" || *archive == "" {
		fmt.Fprintln(os.Stderr, "error: --game and --archive are required")
		fs.Usage()
		return 2
	}
	pbPolicy, ok := parsePolicy(*policy)
	if !ok {
		fmt.Fprintf(os.Stderr, "error: unknown policy %q (want abort|skip|rename|overwrite)\n", *policy)
		return 2
	}
	archivePath, err := filepath.Abs(*archive)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	conn, err := dialDaemon(*socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer conn.Close()
	client := pb.NewGorganizerClient(conn)

	if *dryRun {
		return runImportPreview(client, *gameID, archivePath)
	}

	stream, err := client.ImportInstance(context.Background(), &pb.ImportInstanceRequest{
		GameId:      *gameID,
		ArchivePath: archivePath,
		Policy:      pbPolicy,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: ImportInstance RPC: %v\n", err)
		return 1
	}
	return drainTransferStream(stream, "import")
}

// runImportPreview prints the archive manifest and per-item collisions.
func runImportPreview(client pb.GorganizerClient, gameID, archivePath string) int {
	ctx := context.Background()
	resp, err := client.PreviewImport(ctx, &pb.PreviewImportRequest{
		GameId:      gameID,
		ArchivePath: archivePath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: PreviewImport RPC: %v\n", err)
		return 1
	}
	fmt.Printf("archive: %s\n", archivePath)
	fmt.Printf("game: %s  schema: v%d  exported: %s  gorganizer: %s\n",
		resp.GetGameId(), resp.GetSchemaVersion(), resp.GetExportedAt(), resp.GetGorganizerVersion())
	fmt.Printf("includes overwrite: %v  includes game settings: %v\n",
		resp.GetIncludesOverwrite(), resp.GetIncludesGameSettings())
	fmt.Printf("mods (%d):\n", len(resp.GetMods()))
	for _, m := range resp.GetMods() {
		marker := ""
		if m.GetCollision() {
			marker = "  [COLLISION]"
		}
		fmt.Printf("  %s  (%d files, %d bytes)%s\n",
			m.GetFolder(), m.GetFileCount(), m.GetTotalBytes(), marker)
	}
	fmt.Printf("profiles (%d):\n", len(resp.GetProfiles()))
	for _, p := range resp.GetProfiles() {
		marker := ""
		if p.GetCollision() {
			marker = "  [COLLISION]"
		}
		fmt.Printf("  %s%s\n", p.GetName(), marker)
	}
	return 0
}

// drainTransferStream prints progress lines until the summary or an error arrives.
func drainTransferStream(stream grpc.ServerStreamingClient[pb.TransferEvent], verb string) int {
	for {
		evt, err := stream.Recv()
		if err == io.EOF {
			return 0
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s failed: %v\n", verb, err)
			return 1
		}
		if p := evt.GetProgress(); p != nil {
			fmt.Printf("[%s] %s (%d/%d items, %d/%d bytes)\n",
				p.GetStep(), p.GetCurrentItem(),
				p.GetItemsDone(), p.GetItemsTotal(),
				p.GetBytesDone(), p.GetBytesTotal())
			continue
		}
		if s := evt.GetSummary(); s != nil {
			printTransferSummary(s, verb)
		}
	}
}

func printTransferSummary(s *pb.TransferSummary, verb string) {
	fmt.Printf("%s complete: %d mods exported, %d mods imported, %d profiles transferred\n",
		verb, s.GetModsExported(), s.GetModsImported(), s.GetProfilesTransferred())
	if len(s.GetSkipped()) > 0 {
		fmt.Printf("skipped: %s\n", strings.Join(s.GetSkipped(), ", "))
	}
	if len(s.GetRenamed()) > 0 {
		keys := make([]string, 0, len(s.GetRenamed()))
		for k := range s.GetRenamed() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("renamed: %s -> %s\n", k, s.GetRenamed()[k])
		}
	}
	if s.GetOutputPath() != "" {
		fmt.Printf("archive: %s\n", s.GetOutputPath())
	}
}

func parsePolicy(s string) (pb.TransferCollisionPolicy, bool) {
	switch strings.ToLower(s) {
	case "abort":
		return pb.TransferCollisionPolicy_TRANSFER_POLICY_ABORT, true
	case "skip":
		return pb.TransferCollisionPolicy_TRANSFER_POLICY_SKIP, true
	case "rename":
		return pb.TransferCollisionPolicy_TRANSFER_POLICY_RENAME, true
	case "overwrite":
		return pb.TransferCollisionPolicy_TRANSFER_POLICY_OVERWRITE, true
	default:
		return pb.TransferCollisionPolicy_TRANSFER_POLICY_ABORT, false
	}
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
