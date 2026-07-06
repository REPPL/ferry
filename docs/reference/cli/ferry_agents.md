## ferry agents

Onboard project repos and migrate agent-instruction setups

### Synopsis

Companion commands for the agents domain.

The domain itself deploys through the normal lifecycle: enable it with
"agents = true" under [manage], then ferry apply / status / diff / restore
handle the per-harness instruction files, the optional devtree workspace file,
and the ~/.claude skills/agents/hooks assets. These subcommands cover the two
one-off operations around it: scaffolding a project repo, and adopting an
existing symlink-based instruction directory into ferry.

### Options

```
  -h, --help   help for agents
```

### SEE ALSO

* [ferry](ferry.md)	 - Carries your terminal, dotfiles, and dependencies across machines
* [ferry agents adopt](ferry_agents_adopt.md)	 - Migrate an existing symlink-based instruction directory into ferry
* [ferry agents scaffold](ferry_agents_scaffold.md)	 - Set a project repo up for the multi-tool agent pipeline

