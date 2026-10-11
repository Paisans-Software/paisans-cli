package validate

import (
	"fmt"
	"regexp"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// channelTag is a toolkit channel: stack-<N>, the latest release of a fork
// promoted for the toolkit's template contract N.
var channelTag = regexp.MustCompile(`^stack-([0-9]+)$`)

// channelOf is the channel a reference follows (Eg: stack-1), empty when its
// tag is not one. A digest beside the tag does not change which channel the
// tag names.
func channelOf(ref string) string {
	parsed := kinds.ParseReference(ref)
	if parsed.Digest != "" {
		parsed = kinds.ParseReference(parsed.Name)
	}
	if channelTag.MatchString(parsed.Tag) {
		return parsed.Tag
	}
	return ""
}

// kindDefault is the image a kind's service runs when the configuration
// names none. A test replaces it.
var kindDefault = func(kind config.Kind, service string) string {
	for _, s := range kinds.Services(kind) {
		if s.Name == service {
			return s.Image
		}
	}
	return ""
}

// imageChannels warns about an app whose image follows a channel other than
// the one its kind defaults to.
//
// A channel number is the template contract the toolkit renders: stack-2 is
// promoted only for a release that needs something rendered that a stack-1
// toolkit does not render, or stops reading something it does. Following
// another channel is how a fork's next contract is tried ahead of a toolkit
// release, so it is accepted; but this toolkit still renders its own
// channel's contract, and the app may not start on what it renders. That is
// risky rather than incoherent, so it is a warning.
func (c *checker) imageChannels() {
	for _, name := range c.cfg.AppNames() {
		app := c.cfg.Apps[name]
		for _, service := range sortedKeys(app.Images) {
			declared := app.Images[service]
			def := kindDefault(app.Kind, service)
			want, got := channelOf(def), channelOf(declared)
			if want == "" || got == "" || want == got {
				continue
			}
			c.warn("image-channel-differs", fmt.Sprintf("apps.%s.images.%s", name, service),
				"image follows another channel than this toolkit renders",
				"is %q, which follows %s, and the %s kind defaults to %s. This toolkit renders %s's contract, so a release promoted to %s may need settings it does not render, or stop reading ones it does. Use it to try a toolkit release ahead of time, and return to the default once the toolkit names %s.",
				declared, got, app.Kind, def, want, got, got)
		}
	}
}
