package http

// A2A Agent Card structures based on A2A Protocol Specification
// https://a2a-protocol.org/latest/specification/

// AgentCard represents the A2A agent card that describes an agent's capabilities,
// skills, and interaction requirements.
type AgentCard struct {
	Name                string                     `json:"name"`
	Description         string                     `json:"description"`
	SupportedInterfaces []AgentInterface           `json:"supportedInterfaces"`
	Provider            *AgentProvider             `json:"provider,omitempty"`
	Version             string                     `json:"version"`
	DocumentationURL    string                     `json:"documentationUrl,omitempty"`
	Capabilities        AgentCapabilities          `json:"capabilities"`
	SecuritySchemes     map[string]SecurityScheme  `json:"securitySchemes,omitempty"`
	Security            []SecurityRequirement      `json:"security,omitempty"`
	DefaultInputModes   []string                   `json:"defaultInputModes"`
	DefaultOutputModes  []string                   `json:"defaultOutputModes"`
	Skills              []AgentSkill               `json:"skills"`
	Tools               []AgentTool                `json:"tools,omitempty"`
	Signatures          []AgentCardSignature       `json:"signatures,omitempty"`
	IconURL             string                     `json:"iconUrl,omitempty"`
}

// AgentInterface declares a combination of a target URL, transport and protocol version.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	Tenant          string `json:"tenant,omitempty"`
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentProvider represents the service provider of an agent.
type AgentProvider struct {
	URL          string `json:"url"`
	Organization string `json:"organization"`
}

// AgentCapabilities defines optional capabilities supported by an agent.
type AgentCapabilities struct {
	Streaming         bool             `json:"streaming,omitempty"`
	PushNotifications bool             `json:"pushNotifications,omitempty"`
	Extensions        []AgentExtension `json:"extensions,omitempty"`
	ExtendedAgentCard bool             `json:"extendedAgentCard,omitempty"`
}

// AgentExtension represents a protocol extension supported by an agent.
type AgentExtension struct {
	URI         string                 `json:"uri"`
	Description string                 `json:"description,omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Params      map[string]interface{} `json:"params,omitempty"`
}

// AgentSkill represents a distinct capability or function that an agent can perform.
type AgentSkill struct {
	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Description          string                `json:"description"`
	Tags                 []string              `json:"tags"`
	Examples             []string              `json:"examples,omitempty"`
	InputModes           []string              `json:"inputModes,omitempty"`
	OutputModes          []string              `json:"outputModes,omitempty"`
	SecurityRequirements []SecurityRequirement `json:"securityRequirements,omitempty"`
}

// AgentTool represents a tool/function that the agent can execute.
type AgentTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}


// SecurityScheme defines a security scheme for authenticating with an agent.
type SecurityScheme struct {
	APIKeySecurityScheme        *APIKeySecurityScheme        `json:"apiKeySecurityScheme,omitempty"`
	HTTPAuthSecurityScheme      *HTTPAuthSecurityScheme      `json:"httpAuthSecurityScheme,omitempty"`
	OAuth2SecurityScheme        *OAuth2SecurityScheme        `json:"oauth2SecurityScheme,omitempty"`
	OpenIdConnectSecurityScheme *OpenIdConnectSecurityScheme `json:"openIdConnectSecurityScheme,omitempty"`
	MutualTlsSecurityScheme     *MutualTlsSecurityScheme     `json:"mtlsSecurityScheme,omitempty"`
}

// APIKeySecurityScheme defines API key-based authentication.
type APIKeySecurityScheme struct {
	Description string `json:"description,omitempty"`
	Location    string `json:"location"`
	Name        string `json:"name"`
}

// HTTPAuthSecurityScheme defines HTTP authentication (Basic, Bearer, etc.).
type HTTPAuthSecurityScheme struct {
	Description  string `json:"description,omitempty"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// OAuth2SecurityScheme defines OAuth 2.0 authentication.
type OAuth2SecurityScheme struct {
	Description       string     `json:"description,omitempty"`
	Flows             OAuthFlows `json:"flows"`
	OAuth2MetadataURL string     `json:"oauth2MetadataUrl,omitempty"`
}

// OAuthFlows defines configuration for supported OAuth 2.0 flows.
type OAuthFlows struct {
	AuthorizationCode *AuthorizationCodeOAuthFlow `json:"authorizationCode,omitempty"`
	ClientCredentials *ClientCredentialsOAuthFlow `json:"clientCredentials,omitempty"`
	DeviceCode        *DeviceCodeOAuthFlow        `json:"deviceCode,omitempty"`
}

// AuthorizationCodeOAuthFlow defines OAuth 2.0 Authorization Code flow.
type AuthorizationCodeOAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl"`
	TokenURL         string            `json:"tokenUrl"`
	RefreshURL       string            `json:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes"`
	PKCERequired     bool              `json:"pkceRequired,omitempty"`
}

// ClientCredentialsOAuthFlow defines OAuth 2.0 Client Credentials flow.
type ClientCredentialsOAuthFlow struct {
	TokenURL   string            `json:"tokenUrl"`
	RefreshURL string            `json:"refreshUrl,omitempty"`
	Scopes     map[string]string `json:"scopes"`
}

// DeviceCodeOAuthFlow defines OAuth 2.0 Device Code flow.
type DeviceCodeOAuthFlow struct {
	DeviceAuthorizationURL string            `json:"deviceAuthorizationUrl"`
	TokenURL               string            `json:"tokenUrl"`
	RefreshURL             string            `json:"refreshUrl,omitempty"`
	Scopes                 map[string]string `json:"scopes"`
}

// OpenIdConnectSecurityScheme defines OpenID Connect authentication.
type OpenIdConnectSecurityScheme struct {
	Description      string `json:"description,omitempty"`
	OpenIdConnectURL string `json:"openIdConnectUrl"`
}

// MutualTlsSecurityScheme defines mTLS authentication.
type MutualTlsSecurityScheme struct {
	Description string `json:"description,omitempty"`
}

// SecurityRequirement represents a security requirement for contacting the agent.
type SecurityRequirement map[string][]string

// AgentCardSignature represents a JWS signature of an AgentCard.
type AgentCardSignature struct {
	Protected string                 `json:"protected"`
	Signature string                 `json:"signature"`
	Header    map[string]interface{} `json:"header,omitempty"`
}
