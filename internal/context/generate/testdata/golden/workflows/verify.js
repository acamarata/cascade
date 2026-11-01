// cascade:managed:begin workflow-verify
export const meta = {
  name: 'cascade-verify',
  description: 'Run the checks Cascade detected for this repository and report each result',
  phases: [{ title: 'Verify' }],
}

const report = await agent(
  'Read .claude/workflows/cascade-workflows.json. For every scope it lists, run that scope\'s build, test and lint commands from the scope directory, one at a time, and change no file. Report each command, its exit status and the first failing line of its output.',
  { label: 'verify', phase: 'Verify' },
)

return { report }
// cascade:managed:end
