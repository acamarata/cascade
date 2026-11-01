// cascade:managed:begin workflow-review
export const meta = {
  name: 'cascade-review',
  description: 'Review the current change on three axes, then confirm each finding',
  phases: [{ title: 'Review' }, { title: 'Confirm' }],
}

const AXES = [
  { key: 'correctness', ask: 'bugs, wrong results and missed edge cases' },
  { key: 'security', ask: 'unsafe input handling, leaked secrets and widened permissions' },
  { key: 'tests', ask: 'behaviour the change adds that no test covers' },
]

const reviews = await parallel(AXES.map(a => () =>
  agent(
    'Review the uncommitted change in this repository for ' + a.ask + '. Change no file. List each finding with its file, line and a one-sentence reason.',
    { label: 'review:' + a.key, phase: 'Review' },
  ),
))

const confirmed = await agent(
  'Here are review findings:\n\n' + reviews.join('\n\n') + '\n\nCheck each one against the code and the commands in .claude/workflows/cascade-workflows.json. Change no file. Keep only the findings you can show are real, each with its evidence.',
  { label: 'confirm', phase: 'Confirm' },
)

return { confirmed }
// cascade:managed:end
