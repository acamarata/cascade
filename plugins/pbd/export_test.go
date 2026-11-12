package pbd

// Names the external registry test needs. It cannot sit in package pbd
// because internal/plugins imports this package.
var (
	PluginIDForTest            = pluginID
	ValidateCommandNameForTest = validateCommandName
	CommandNamesForTest        = []string{
		validateCommandName, lintCommandName,
		createCommandName, editCommandName, moveCommandName,
		statusCommandName, boardCommandName,
		claimCommandName, stepCommandName, doneCommandName,
		dispatchCommandName,
	}
)
