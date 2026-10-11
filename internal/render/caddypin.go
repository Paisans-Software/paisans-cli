package render

import (
	"fmt"
	"strings"
)

// PinCaddyImage returns an infrastructure compose file with its Caddy image
// line naming pinned instead of rendered, which is what Plan.CaddyImages
// holds for the site.
//
// It lives here rather than in apply because the line is this package's: the
// infrastructure template writes the image as `image: <reference>` on a line
// of its own, under the caddy service, and nothing else in the file names the
// gateway's image. Exactly one such line must exist. None, or two, means the
// template changed under this function, and pinning the wrong service, or
// none, would deploy a gateway the operator was not shown.
func PinCaddyImage(compose, rendered, pinned string) (string, error) {
	line := "    image: " + rendered + "\n"
	if n := strings.Count(compose, line); n != 1 {
		return "", fmt.Errorf("the infrastructure compose file names the Caddy image %s on %d lines, where one was rendered; the template and PinCaddyImage disagree", rendered, n)
	}
	return strings.Replace(compose, line, "    image: "+pinned+"\n", 1), nil
}
