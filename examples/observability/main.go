// Command observability logs each attempt of a saga as one JSON line with sagas.Slog.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
