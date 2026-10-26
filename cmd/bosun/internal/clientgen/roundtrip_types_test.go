package clientgen_test

// These types are shared by the round-trip test's server and, copied into a
// contracts package, by the generated client. Keep this file free of imports.

// Echo reports what the server bound and received.
type Echo struct {
	ID          string  `json:"id,omitempty"`
	Limit       int     `json:"limit,omitempty"`
	Verbose     bool    `json:"verbose,omitempty"`
	Token       string  `json:"token,omitempty"`
	Dry         bool    `json:"dry,omitempty"`
	Ratio       float64 `json:"ratio,omitempty"`
	Name        string  `json:"name,omitempty"`
	Age         int     `json:"age,omitempty"`
	Title       string  `json:"title,omitempty"`
	Count       int     `json:"count,omitempty"`
	Q           string  `json:"q,omitempty"`
	Text        string  `json:"text,omitempty"`
	Query       string  `json:"query,omitempty"`
	ContentType string  `json:"content_type,omitempty"`
	Body        string  `json:"body,omitempty"`
}

// GetUserIn has only path, query and header fields, for a GET.
type GetUserIn struct {
	ID      string `path:"id"`
	Limit   int    `query:"limit"`
	Verbose bool   `query:"verbose"`
	Token   string `header:"X-Token"`
}

// UpdateUserIn mixes bound fields with JSON body fields.
type UpdateUserIn struct {
	ID    string  `path:"id" json:"id"`
	Dry   bool    `query:"dry"`
	Token string  `header:"X-Token" json:"token"`
	Ratio float64 `query:"ratio" json:"-"`
	Name  string  `json:"name"`
	Age   int     `json:"age"`
}

// FormIn is bound from a form body and the query string.
type FormIn struct {
	Title string `form:"title"`
	Count int    `form:"count"`
	Q     string `query:"q"`
}
