// cascade:managed:begin workflow-implement
export const meta = {
  name: 'cascade-implement',
  description: 'Make the change the arguments describe, then run the detected checks',
  phases: [{ title: 'Implement' }, { title: 'Verify' }],
}

const task = String(args ?? '')

const change = await agent(
  'Make this change in the repository, keeping to the scope it names and the style of the surrounding code:\n\n' + task + '\n\nThen list the files you changed.',
  { label: 'implement', phase: 'Implement' },
)

const checks = await agent(
  'Read .claude/workflows/cascade-workflows.json and run every build, test and lint command it lists from its scope directory. Change no file. Report each command and its exit status.\n\nThe change made:\n\n' + change,
  { label: 'verify', phase: 'Verify' },
)

return { change, checks }
// cascade:managed:end
