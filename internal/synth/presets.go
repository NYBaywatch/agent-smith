package synth

// presets is the built-in check set: stable, unauthenticated endpoints that
// answer quickly and represent the services people actually depend on. API
// endpoints that require credentials are still useful — a 401 proves the
// service is up and reachable, which is why ExpectStatus is left at 0 (any
// status below 500 counts as OK) unless a specific code is the only healthy
// answer (e.g. Google's generate_204).
var presets = []Check{
	// --- CDN edges: which provider's nearest PoP serves us, and how fast. ---
	{Name: "Cloudflare", URL: "https://www.cloudflare.com/cdn-cgi/trace", Category: CategoryCDN, Provider: "Cloudflare"},
	{Name: "Fastly", URL: "https://www.fastly.com/", Category: CategoryCDN, Provider: "Fastly"},
	{Name: "CloudFront", URL: "https://d1.awsstatic.com/", Category: CategoryCDN, Provider: "CloudFront"},
	{Name: "Akamai", URL: "https://www.akamai.com/", Category: CategoryCDN, Provider: "Akamai"},
	{Name: "Google Edge", URL: "https://www.gstatic.com/generate_204", Category: CategoryCDN, Provider: "Google", ExpectStatus: 204},

	// --- Cloud providers: a regional storage front door per provider. ---
	{Name: "AWS S3 (us-east-1)", URL: "https://s3.amazonaws.com/", Category: CategoryCloud, Provider: "AWS"},
	{Name: "Azure", URL: "https://azure.microsoft.com/favicon.ico", Category: CategoryCloud, Provider: "Azure"},
	{Name: "Google Cloud Storage", URL: "https://storage.googleapis.com/", Category: CategoryCloud, Provider: "GCP"},
	{Name: "Oracle Cloud (Ashburn)", URL: "https://objectstorage.us-ashburn-1.oraclecloud.com/", Category: CategoryCloud, Provider: "OCI"},

	// --- SaaS: the apps a work day (or an evening) depends on. ---
	{Name: "Microsoft 365", URL: "https://outlook.office.com/", Category: CategorySaaS, Provider: "Microsoft"},
	{Name: "Microsoft Teams", URL: "https://teams.microsoft.com/favicon.ico", Category: CategorySaaS, Provider: "Microsoft"},
	{Name: "Google Workspace", URL: "https://workspace.google.com/", Category: CategorySaaS, Provider: "Google"},
	{Name: "Zoom", URL: "https://zoom.us/", Category: CategorySaaS, Provider: "Zoom"},
	{Name: "Slack", URL: "https://slack.com/", Category: CategorySaaS, Provider: "Slack"},
	{Name: "GitHub API", URL: "https://api.github.com/", Category: CategorySaaS, Provider: "GitHub"},
	{Name: "Steam Store", URL: "https://store.steampowered.com/", Category: CategorySaaS, Provider: "Valve"},

	// --- AI APIs: unauthenticated calls return 401, which still proves reachability. ---
	{Name: "OpenAI API", URL: "https://api.openai.com/v1/models", Category: CategoryAI, Provider: "OpenAI"},
	{Name: "Anthropic API", URL: "https://api.anthropic.com/v1/models", Category: CategoryAI, Provider: "Anthropic"},
}

// Presets returns a copy of the built-in check set (all enabled).
func Presets() []Check {
	return append([]Check(nil), presets...)
}

// PresetsByCategory returns the built-in checks in one category.
func PresetsByCategory(c Category) []Check {
	var out []Check
	for _, p := range presets {
		if p.Category == c {
			out = append(out, p)
		}
	}
	return out
}
