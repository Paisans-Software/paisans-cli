package ui

// Name is a name as a message gives it: an app, a site, a service, a
// configuration path or an image reference, set off in backticks, so that a
// service called app reads as `app` and not as the word.
func Name(s string) string { return "`" + s + "`" }
