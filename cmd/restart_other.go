//go:build !unix

package cmd

import "errors"

// restartSelf cannot replace the process here: start m9s again by hand.
func restartSelf() error {
	return errors.New("config saved: start m9s again to use it")
}
