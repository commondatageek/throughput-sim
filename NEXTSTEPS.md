# Next Steps
- CFD

## Simulation Report
- For the "how many days" report, show the date "today + N days" so we can see where it would land.
  - Maybe make a `--relative-to {date}` flag that defaults to today
- Also, give the number of sprints that number of days is equivalent to?
- Since these are calendar days, maybe give the number of work days/weeks/sprints?

## Performance
- Cache for issue data
- Concurrency with goroutines to make simulation go faster
- Show a progress bar for the simulation
- Calculate a gradient on deltas between simulation runs, and auto-stop when we've converged
  - Prove that running it longer doesn't lead to different outcomes

## Representative Population of Throughput
- Carve out multiple time periods
  - Right now just start and end date
  - But we should be able to say "This month and that month"
- Have a sliding window of "historical data" and "data from since we started this project", with historical data eventually sliding off of the window.

## CFDs
- Because how else can we know if our predictions are good or not?
- And it drives home the point that this is a system that needs to be tuned, not an outcomes that needs tampering

## Source abstraction
Use issue data from multiple source types:
- one or more text files
  - JSON
  - CSV
  - other formats? XSLX?
- authenticated connection to project management backend

## Multiple project management backends
- Create a generic data model
- Create a project-management backend integration abstraction
  - So far, just Get issues with filtering
- Concrete implementations handle authentication, etc.
- Create an integration for Linear
- Create an integration for Jira

## Simulation
- An optional step in the simulation that makes a daily draw on a distribution of "added tickets per day" in order to model how scope increases over time.
- A TUI for simulation options
- Allow more than one JSON source file for issue data
- A YAML configuration for the simulation

- list of simulation options:
  - -engineers N : Anonymous N-engineer team; samples are pooled across all engineers
  - -team : Named engineers; each samples from their own history
  - -whole-team : Uses the team's aggregate daily throughput as a single unit
