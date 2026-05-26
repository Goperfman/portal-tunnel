package types

type X402Config struct {
	Network            string `json:"network,omitempty" koanf:"network"`
	Price              string `json:"price,omitempty" koanf:"price"`
	PayTo              string `json:"pay_to,omitempty" koanf:"pay_to"`
	FacilitatorURL     string `json:"facilitator_url,omitempty" koanf:"facilitator_url"`
	Resource           string `json:"resource,omitempty" koanf:"resource"`
	MimeType           string `json:"mime_type,omitempty" koanf:"mime_type"`
	Testnet            bool   `json:"testnet,omitempty" koanf:"testnet"`
	MaxTimeoutSeconds  int    `json:"max_timeout_seconds,omitempty" koanf:"max_timeout_seconds"`
	PaymentTimeoutSecs int    `json:"payment_timeout_seconds,omitempty" koanf:"payment_timeout_seconds"`
}

func (c X402Config) Empty() bool {
	return c.Network == "" &&
		c.Price == "" &&
		c.PayTo == "" &&
		c.FacilitatorURL == "" &&
		c.Resource == "" &&
		c.MimeType == "" &&
		!c.Testnet &&
		c.MaxTimeoutSeconds == 0 &&
		c.PaymentTimeoutSecs == 0
}
