package x402

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	facilitatorapi "github.com/gosuda/x402-facilitator/api"
	facilitatorcore "github.com/gosuda/x402-facilitator/facilitator"

	"github.com/gosuda/portal-tunnel/v2/types"
)

const (
	MainnetNetwork = "sui:mainnet"
	TestnetNetwork = "sui:testnet"
)

var networkDisplayNames = map[string]string{
	MainnetNetwork: "Sui Mainnet",
	TestnetNetwork: "Sui Testnet",
}

func Network(testnet bool) string {
	if testnet {
		return TestnetNetwork
	}
	return MainnetNetwork
}

func NetworkDisplayName(network string) string {
	return networkDisplayNames[strings.TrimSpace(strings.ToLower(network))]
}

type FacilitatorConfig struct {
	Testnet bool
}

func MountFacilitator(mux *http.ServeMux, cfg FacilitatorConfig) error {
	if mux == nil {
		return errors.New("x402 facilitator requires an api mux")
	}
	facilitator, err := facilitatorcore.NewSuiFacilitator(Network(cfg.Testnet), "", "")
	if err != nil {
		return fmt.Errorf("create sui x402 facilitator: %w", err)
	}
	mux.Handle(types.PathX402Facilitator+"/", http.StripPrefix(types.PathX402Facilitator, facilitatorapi.NewServer(facilitator)))
	return nil
}
