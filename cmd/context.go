package cmd

// Context and summary use the same source selection and bounded representation.
// Neither command implicitly unions archives or prints persistent notes.
var contextCmd = newTrafficSummaryCommand("context")

func init() {
	rootCmd.AddCommand(contextCmd)
}
