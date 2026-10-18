// Command server runs the Ask Panurus HTTP API and web page.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atharrva01/ask-panurus/internal/answer"
	"github.com/atharrva01/ask-panurus/internal/embed"
	"github.com/atharrva01/ask-panurus/internal/index"
	"github.com/atharrva01/ask-panurus/internal/llm/anthropic"
	"github.com/atharrva01/ask-panurus/internal/retrieve"
	"github.com/atharrva01/ask-panurus/internal/server"
)

func main() {
	addr := flag.String("addr", envOr("ASK_PANURUS_ADDR", "127.0.0.1:8080"), "listen address")
	db := flag.String("db", os.Getenv("ASK_PANURUS_DB"), "Postgres URL")
	tei := flag.String("tei", os.Getenv("ASK_PANURUS_TEI"), "TEI embedding server URL; empty disables vector search")
	model := flag.String("model", envOr("ASK_PANURUS_MODEL", ""), "Claude model ID (default claude-opus-5-5)")
	effort := flag.String("effort", envOr("ASK_PANURUS_EFFORT", "medium"), "Claude effort: low, medium, high, xhigh or max")
	refuse := flag.Float64("refuse-below", 0.65, "refuse when the best retrieval coverage is below this")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := index.Open(ctx, *db)
	if err != nil {
		log.Error("open index", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	r := &retrieve.Retriever{Store: store}
	mode := retrieve.ModeKeyword
	if *tei != "" {
		client := embed.New(*tei)
		info, err := client.Info(ctx)
		if err != nil {
			log.Error("embedding server", "err", err)
			os.Exit(1)
		}
		r.Embedder, r.Model, mode = client, info.ModelID, retrieve.ModeHybrid
	}
	s := &server.Server{Store: store, Retriever: r, Log: log}
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		s.Pipeline = &answer.Pipeline{Retriever: r, LLM: anthropic.New(*model, *effort), RefuseBelow: *refuse, Mode: mode}
	} else {
		log.Warn("no ANTHROPIC_API_KEY: serving search only")
	}

	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("listening", "addr", *addr, "mode", mode, "answers", s.Pipeline != nil)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
