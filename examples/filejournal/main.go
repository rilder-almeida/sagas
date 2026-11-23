// Command filejournal writes a saga journal to a JSON-lines file and reads it back.
// Use one journal value for a directory, in one process. Two values that append the same
// Seq at the same time can both succeed, and the next read of that execution then fails.
package main

import "os"

func main() { os.Exit(execute(os.Stdout, os.Stderr)) }
