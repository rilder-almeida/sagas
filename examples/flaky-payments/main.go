// Command flaky-payments charges four cards through a provider that fails, refuses and stops answering.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
