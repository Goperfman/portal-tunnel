package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal-tunnel/v2/sdk"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

func main() {
	log.Logger = log.Output(zerolog.NewConsoleWriter())
	if err := utils.RunCommands(os.Args[1:], os.Stdout, os.Stderr, printRootUsage, map[string]utils.CommandFunc{
		"":    runTCPCommand,
		"tcp": runTCPCommand,
		"udp": runUDPCommand,
		"help": utils.MakeHelpCommand(printRootUsage, []utils.HelpTopic{
			{Name: "tcp", Usage: printTCPUsage},
			{Name: "udp", Usage: printUDPUsage},
		}),
	}); err != nil {
		log.Error().Err(err).Msg("demo command failed")
		os.Exit(1)
	}
}

type demoConfig struct {
	relayURLs       string
	discovery       bool
	banMITM         bool
	identityPath    string
	identityJSON    string
	addr            string
	name            string
	desc            string
	tags            string
	owner           string
	hide            bool
	thumbnail       string
	maxActiveRelays int
	x402            types.X402Config
}

// registerConnectivityFlags registers the relay, discovery, identity, and
// owner flags that are shared across TCP and UDP demo commands.
func registerConnectivityFlags(fs *flag.FlagSet, cfg *demoConfig, defaultRelays string) {
	utils.StringFlagEnv(fs, &cfg.relayURLs, "relays", defaultRelays, "additional relay API URLs (comma-separated; scheme omitted defaults to https; merged with bootstrap relays when discovery is enabled)", "RELAYS")
	utils.BoolFlagEnv(fs, &cfg.discovery, "discovery", true, "include bootstrap relays and enable discovery", "DISCOVERY")
	utils.BoolFlagEnv(fs, &cfg.banMITM, "ban-mitm", false, "ban relay when the MITM self-probe detects TLS termination", "BAN_MITM")
	utils.StringFlagEnv(fs, &cfg.identityPath, "identity-path", "identity.json", "identity json file path", "IDENTITY_PATH")
	utils.StringFlagEnv(fs, &cfg.identityJSON, "identity-json", "", "identity json payload; overrides --identity-path contents and is persisted there when both are set", "IDENTITY_JSON")
	utils.IntFlagEnv(fs, &cfg.maxActiveRelays, "max-active-relays", 3, nil, "maximum number of auto-selected relays to keep connected; explicit --relays are always included", "MAX_ACTIVE_RELAYS")
	utils.StringFlag(fs, &cfg.owner, "owner", "PortalApp Developer", "lease owner")
}

func runTCPCommand(args []string) error {
	cfg := demoConfig{}

	fs := utils.NewFlagSet("demo-app", printTCPUsage)
	registerConnectivityFlags(fs, &cfg, "https://gosunuts.xyz")
	utils.StringFlag(fs, &cfg.addr, "addr", "127.0.0.1:8092", "local demo HTTP listen address (host:port or URL; disable if empty)")
	utils.StringFlag(fs, &cfg.name, "name", "demo-app", "public hostname prefix (single DNS label)")
	utils.StringFlag(fs, &cfg.desc, "description", "Portal demo connectivity app", "lease description")
	utils.StringFlag(fs, &cfg.tags, "tags", "demo,connectivity,activity,cloud,sun,morning", "comma-separated lease tags")
	utils.StringFlag(fs, &cfg.thumbnail, "thumbnail", "https://picsum.photos/640/360", "lease thumbnail")
	utils.BoolFlag(fs, &cfg.hide, "hide", false, "hide this lease from listings")
	utils.StringFlag(fs, &cfg.x402.FacilitatorURL, "x402-facilitator-url", "", "x402 facilitator URL, such as https://relay.example.com:4017/x402")
	utils.StringFlag(fs, &cfg.x402.Network, "x402-network", "", "x402 payment network, such as eip155:8453")
	utils.StringFlag(fs, &cfg.x402.Price, "x402-price", "", "fallback x402 price for premium content, such as $0.001")
	utils.StringFlag(fs, &cfg.x402.PayTo, "x402-pay-to", "", "x402 recipient address; empty uses the demo app identity address")
	utils.StringFlag(fs, &cfg.x402.Resource, "x402-resource", "", "x402 protected resource path; defaults to /api/premium")
	utils.StringFlag(fs, &cfg.x402.MimeType, "x402-mime-type", "", "x402 protected resource MIME type; defaults to application/json")
	utils.BoolFlag(fs, &cfg.x402.Testnet, "x402-testnet", false, "render the x402 paywall in testnet mode")
	fs.IntVar(&cfg.x402.MaxTimeoutSeconds, "x402-max-timeout", 0, "x402 max payment timeout seconds advertised to clients")
	fs.IntVar(&cfg.x402.PaymentTimeoutSecs, "x402-payment-timeout", 0, "x402 middleware verify/settle timeout seconds")

	if err := utils.ParseFlagSet(fs, args, printTCPUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "demo-app"); err != nil {
		printTCPUsage(os.Stderr)
		return err
	}
	normalizedName, err := utils.NormalizeDNSLabel(cfg.name)
	if err != nil {
		return fmt.Errorf("invalid --name value: %w", err)
	}
	cfg.name = normalizedName
	if !cfg.x402.Empty() {
		switch {
		case strings.TrimSpace(cfg.x402.FacilitatorURL) == "":
			return errors.New("--x402-facilitator-url is required when x402 is enabled")
		case strings.TrimSpace(cfg.x402.Network) == "":
			return errors.New("--x402-network is required when x402 is enabled")
		case cfg.x402.MaxTimeoutSeconds < 0:
			return errors.New("--x402-max-timeout cannot be negative")
		case cfg.x402.PaymentTimeoutSecs < 0:
			return errors.New("--x402-payment-timeout cannot be negative")
		}
		if strings.TrimSpace(cfg.x402.Resource) == "" {
			cfg.x402.Resource = "/api/premium"
		}
		if strings.TrimSpace(cfg.x402.MimeType) == "" {
			cfg.x402.MimeType = "application/json"
		}
	}

	ctx, stop := utils.SignalContext()
	defer stop()

	return runTCPDemo(ctx, cfg)
}

func runUDPCommand(args []string) error {
	cfg := demoConfig{}
	fs := utils.NewFlagSet("demo-app-udp", printUDPUsage)

	registerConnectivityFlags(fs, &cfg, "https://localhost:4017")
	utils.StringFlag(fs, &cfg.name, "name", "demo-udp", "public hostname prefix (single DNS label)")
	utils.StringFlag(fs, &cfg.desc, "description", "Portal demo UDP echo service", "lease description")
	utils.StringFlag(fs, &cfg.tags, "tags", "demo,udp,echo", "comma-separated lease tags")
	utils.StringFlag(fs, &cfg.thumbnail, "thumbnail", "", "lease thumbnail")
	utils.BoolFlag(fs, &cfg.hide, "hide", true, "hide this lease from listings")

	if err := utils.ParseFlagSet(fs, args, printUDPUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "udp"); err != nil {
		printUDPUsage(os.Stderr)
		return err
	}
	normalizedName, err := utils.NormalizeDNSLabel(cfg.name)
	if err != nil {
		return fmt.Errorf("invalid --name value: %w", err)
	}
	cfg.name = normalizedName

	ctx, stop := utils.SignalContext()
	defer stop()

	return runUDPDemo(ctx, cfg)
}

func runTCPDemo(ctx context.Context, cfg demoConfig) error {
	metadata := types.LeaseMetadata{
		Description: cfg.desc,
		Tags:        utils.SplitCSV(cfg.tags),
		Owner:       cfg.owner,
		Thumbnail:   cfg.thumbnail,
		Hide:        cfg.hide,
	}
	exposure, err := sdk.Expose(ctx, sdk.ExposeConfig{
		RelayURLs:       utils.SplitCSV(cfg.relayURLs),
		Discovery:       cfg.discovery,
		Identity:        types.Identity{Name: cfg.name},
		IdentityPath:    cfg.identityPath,
		IdentityJSON:    cfg.identityJSON,
		BanMITM:         cfg.banMITM,
		MaxActiveRelays: cfg.maxActiveRelays,
		Metadata:        metadata,
	})
	if err != nil {
		return fmt.Errorf("exposure listen error: %w", err)
	}

	rawAddr := cfg.addr
	cfg.addr, err = utils.NormalizeTargetAddr(cfg.addr)
	if err != nil {
		return fmt.Errorf("invalid --addr value %q: %w", rawAddr, err)
	}
	var x402Config *types.X402Config
	if !cfg.x402.Empty() {
		if strings.TrimSpace(cfg.x402.PayTo) == "" {
			cfg.x402.PayTo = types.X402PayToIdentity
		}
		x402Config = &cfg.x402
	}
	httpHandler, err := newHandler(exposure.Identity(), metadata, x402Config)
	if err != nil {
		return err
	}
	defer exposure.Close()
	err = exposure.RunHTTP(ctx, httpHandler, cfg.addr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		return err
	}

	if ctx.Err() != nil {
		log.Info().Msg("demo app shutting down")
	}
	log.Info().Msg("demo app shutdown complete")
	return nil
}

func runUDPDemo(ctx context.Context, cfg demoConfig) error {
	exposure, err := sdk.Expose(ctx, sdk.ExposeConfig{
		RelayURLs:       utils.SplitCSV(cfg.relayURLs),
		Discovery:       cfg.discovery,
		Identity:        types.Identity{Name: cfg.name},
		IdentityPath:    cfg.identityPath,
		IdentityJSON:    cfg.identityJSON,
		UDPEnabled:      true,
		BanMITM:         cfg.banMITM,
		MaxActiveRelays: cfg.maxActiveRelays,
		Metadata: types.LeaseMetadata{
			Description: cfg.desc,
			Tags:        utils.SplitCSV(cfg.tags),
			Owner:       cfg.owner,
			Thumbnail:   cfg.thumbnail,
			Hide:        cfg.hide,
		},
	})
	if err != nil {
		return fmt.Errorf("exposure listen error: %w", err)
	}
	defer exposure.Close()

	udpAddrs, err := exposure.WaitDatagramReady(ctx)
	if err != nil {
		return fmt.Errorf("wait for udp readiness: %w", err)
	}
	for _, udpAddr := range udpAddrs {
		log.Info().Str("udp_addr", udpAddr).Msg("demo udp relay ready")
	}

	go runUDPEchoLoop(ctx, exposure)

	if err := exposure.RunHTTP(ctx, newUDPInfoHandler(exposure), ""); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		return err
	}

	if ctx.Err() != nil {
		log.Info().Msg("demo udp shutting down")
	}
	log.Info().Msg("demo udp shutdown complete")
	return nil
}

func runUDPEchoLoop(ctx context.Context, exposure *sdk.Exposure) {
	for {
		frame, err := exposure.AcceptDatagram()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Warn().Err(err).Msg("demo udp accept failed")
			return
		}

		payload := bytes.Clone(frame.Payload)
		if len(payload) == 0 {
			payload = []byte("pong")
		}
		frame.Payload = payload
		if err := exposure.SendDatagram(frame); err != nil && ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
			log.Warn().Err(err).Uint32("flow_id", frame.FlowID).Msg("demo udp reply failed")
			return
		}
	}
}

func printRootUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"demo-app [flags]",
			"demo-app tcp [flags]",
			"demo-app udp [flags]",
			"demo-app help",
		},
		[]string{
			"demo-app",
			"demo-app --name my-app",
			"demo-app tcp --addr 127.0.0.1:9000",
			"demo-app udp",
			"demo-app --x402-facilitator-url https://relay.example.com:4017/x402 --x402-network eip155:8453",
		},
	)
}

func printTCPUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"demo-app [flags]",
			"demo-app tcp [flags]",
		},
		[]string{
			"demo-app",
			"demo-app --name my-app",
			"demo-app tcp --addr 127.0.0.1:9000",
			"demo-app --x402-facilitator-url https://relay.example.com:4017/x402 --x402-network eip155:8453",
		},
	)
}

func printUDPUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"demo-app udp [flags]",
		},
		[]string{
			"demo-app udp",
			"demo-app udp --name my-udp-demo",
			"demo-app udp --discovery=true",
		},
	)
}
