package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ayush/cos-lite/internal/browser"
	"github.com/ayush/cos-lite/internal/clientctx"
	"github.com/ayush/cos-lite/internal/codeintel"
	"github.com/ayush/cos-lite/internal/config"
	"github.com/ayush/cos-lite/internal/instructions"
	planpkg "github.com/ayush/cos-lite/internal/plan"
	"github.com/ayush/cos-lite/internal/plugins"
	proc "github.com/ayush/cos-lite/internal/process"
	resourcepkg "github.com/ayush/cos-lite/internal/resources"
	skillpkg "github.com/ayush/cos-lite/internal/skills"
	taskpkg "github.com/ayush/cos-lite/internal/tasks"
	"github.com/ayush/cos-lite/internal/telemetry"
	"github.com/ayush/cos-lite/internal/tools"
	"github.com/ayush/cos-lite/internal/workspace"
)

type daemonServices struct {
	pm        *proc.Manager
	plans     *planpkg.Store
	tasks     *taskpkg.Manager
	telemetry *telemetry.Recorder
	lspPool   *codeintel.Pool

	browserMu       sync.Mutex
	browserPool     *browser.Pool
	browserHeadless bool
}

func newDaemonServices(rec *telemetry.Recorder) *daemonServices {
	pm := proc.NewManager()
	return &daemonServices{pm: pm, plans: &planpkg.Store{}, tasks: taskpkg.NewManager(pm), telemetry: rec, lspPool: codeintel.NewPool(codeintel.DefaultPoolIdleTTL)}
}

func (s *daemonServices) browsers(enabled, headless bool) *browser.Pool {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if !enabled {
		old := s.browserPool
		s.browserPool = nil
		if old != nil {
			go func() {
				time.Sleep(5 * time.Second)
				old.Shutdown()
			}()
		}
		return nil
	}
	if s.browserPool != nil && s.browserHeadless == headless {
		return s.browserPool
	}
	old := s.browserPool
	s.browserPool = browser.NewPool(headless)
	s.browserHeadless = headless
	if old != nil {
		go func() {
			time.Sleep(5 * time.Second)
			old.Shutdown()
		}()
	}
	return s.browserPool
}

func (s *daemonServices) shutdown() {
	s.browserMu.Lock()
	p := s.browserPool
	s.browserPool = nil
	s.browserMu.Unlock()
	if p != nil {
		p.Shutdown()
	}
	if s.lspPool != nil {
		s.lspPool.Close()
	}
}

type runtimeBundle struct {
	cfg                  config.Config
	ws                   *workspace.Workspace
	registry             *tools.Registry
	resources            *resourcepkg.Provider
	instructions         *instructions.Resolver
	skills               *skillpkg.Library
	plugins              *plugins.Manager
	names                []string
	instructionMu        sync.Mutex
	instructionTextCache string
	instructionTextUntil time.Time
}

func buildRuntime(ctx context.Context, cfg config.Config, svc *daemonServices, status func(context.Context) any) (*runtimeBundle, error) {
	enabled := cfg.EnabledProjects()
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no enabled projects")
	}
	roots := make([]workspace.Root, 0, len(enabled))
	names := make([]string, 0, len(enabled))
	for _, p := range enabled {
		roots = append(roots, workspace.Root{Name: p.Name, Path: p.Path})
		names = append(names, p.Name)
	}
	ws, err := workspace.NewRoots(roots)
	if err != nil {
		return nil, err
	}
	reg := tools.NewRegistry()
	mustReg := func(t tools.Tool) error { return reg.Register(t) }
	for _, t := range []tools.Tool{
		(&tools.Reader{WS: ws}).Definition(),
		(&tools.ImageViewer{WS: ws}).Definition(),
		(&tools.Finder{WS: ws}).Definition(),
		(&tools.Patcher{WS: ws}).Definition(),
	} {
		if err := mustReg(t); err != nil {
			return nil, err
		}
	}
	ex := &tools.Executor{WS: ws, PM: svc.pm}
	if err := mustReg(ex.ExecDefinition()); err != nil {
		return nil, err
	}
	if err := mustReg(ex.StdinDefinition()); err != nil {
		return nil, err
	}
	if err := mustReg((&tools.Planner{Store: svc.plans}).Definition()); err != nil {
		return nil, err
	}
	skillLib := &skillpkg.Library{WS: ws, Global: cfg.Skills.Global, Repo: cfg.Skills.Repo}
	if cfg.Skills.Enabled {
		if err := mustReg((&tools.SkillsTool{Library: skillLib}).Definition()); err != nil {
			return nil, err
		}
	}
	if cfg.CodeIntel.Enabled {
		ci := &codeintel.Client{WS: ws, Overrides: cfg.CodeIntel.Overrides, Pool: svc.lspPool}
		if err := mustReg((&tools.CodeIntel{Client: ci}).Definition()); err != nil {
			return nil, err
		}
	}

	pluginManager := &plugins.Manager{}
	pluginCfg, pluginWarnings, err := plugins.LoadCodexConfig()
	if err != nil {
		return nil, err
	}
	for _, warning := range pluginWarnings {
		fmt.Printf("cos-lite: %s\n", warning)
	}
	if len(pluginCfg) > 0 {
		for _, warning := range pluginManager.Register(ctx, reg, pluginCfg) {
			fmt.Printf("cos-lite: %s\n", warning)
		}
	}
	pool := svc.browsers(cfg.Browser, cfg.Headless)
	if cfg.Browser {
		bt := &tools.BrowserTools{Pool: pool}
		for _, t := range bt.Definitions() {
			if err := reg.Register(t); err != nil {
				pluginManager.Close()
				return nil, err
			}
		}
	}
	if err := reg.Register((&tools.ExecMode{Registry: reg}).Definition()); err != nil {
		pluginManager.Close()
		return nil, err
	}
	resolver := &instructions.Resolver{WS: ws}
	resourceProvider := &resourcepkg.Provider{
		WS: ws, Skills: skillLib, Instructions: resolver, Status: status,
		Audit: func(reqCtx context.Context) any {
			return svc.telemetry.Recent(clientctx.Key(reqCtx), 100)
		},
	}
	return &runtimeBundle{cfg: cfg, ws: ws, registry: reg, resources: resourceProvider, instructions: resolver, skills: skillLib, plugins: pluginManager, names: names}, nil
}

func (r *runtimeBundle) instructionText(_ context.Context) string {
	if r == nil || r.ws == nil {
		return ""
	}
	r.instructionMu.Lock()
	if time.Now().Before(r.instructionTextUntil) {
		text := r.instructionTextCache
		r.instructionMu.Unlock()
		return text
	}
	defer r.instructionMu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Ubuntu-first local coding tools. Approved project roots: %s. With multiple projects, use /<project>/... paths explicitly.", r.ws.RootSummary())
	if rootAgents := r.instructions.ServerText(); rootAgents != "" {
		b.WriteString("\n\n# Project instructions (AGENTS.md)\n")
		b.WriteString(rootAgents)
		b.WriteString("\n\nFor nested paths, read cos://projects/<project>/instructions?path=<relative-path> to resolve hierarchical AGENTS.md files.")
	}
	if r.cfg.Skills.Enabled {
		b.WriteString("\n\n# Skills\nUse the skills tool to load a workflow only when relevant. Skills never grant extra permissions.\n")
		b.WriteString(r.skills.CatalogText())
	}
	if r.cfg.CodeIntel.Enabled {
		b.WriteString("\n\n# Code intelligence\nPrefer code_intel for semantic definitions, references, types, diagnostics and rename previews; use find/rg for textual search.")
	}
	b.WriteString("\n\n# Resources\nUse MCP Resources for cos://status, project/Git context, hierarchical instructions, Skills catalog and your recent audit events.")
	text := b.String()
	r.instructionTextCache = text
	r.instructionTextUntil = time.Now().Add(2 * time.Second)
	return text
}

func (r *runtimeBundle) closeDelayed() {
	if r == nil || r.plugins == nil {
		return
	}
	p := r.plugins
	go func() {
		time.Sleep(5 * time.Second)
		p.Close()
	}()
}
