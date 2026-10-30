package commands

func validDecisionBrowser(name string) bool {
	return name == "dia" || name == "arc" || name == "chrome" || name == "any" || name == "headless"
}
