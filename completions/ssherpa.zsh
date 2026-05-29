#compdef ssherpa

_ssherpa() {
  local -a commands
  commands=(
    'add:Add or update an SSH alias'
    'edit:Edit or delete SSH aliases'
    'jump:Connect through ProxyJump hops'
    'proxy:Start a local SOCKS proxy'
    'forward:Open and manage port-forward tunnels'
    'check:Test SSH aliases and saved forwards'
    'authkeys:Manage authorized_keys'
    'theme:Build and save the terminal UI color schema'
    'session:Inspect supervised session records'
    'list:List SSH aliases'
    'show:Show one SSH alias'
    'version:Print build version'
    'help:Show help'
  )

  _arguments -C \
    '1:command:->command' \
    '*::arg:->args'

  case $state in
    command)
      _describe -t commands 'ssherpa commands' commands
      ;;
    args)
      case $words[2] in
        forward)
          if [[ $words[3] == saved ]]; then
            _arguments \
              '3:subcommand:(list show save edit delete rename)' \
              '--state-dir[override state directory]:path:_files' \
              '--config[read SSH config]:path:_files' \
              '--select[SSH alias]:alias' \
              '--local[local bind/port]:local' \
              '--remote[remote host/port]:remote' \
              '--through[jump alias]:alias' \
              '--description[description]:text' \
              '--json[emit JSON]' \
              '--yes[skip confirmation]'
          else
            _arguments \
              '2:subcommand:(list status stop saved)' \
              '--select[SSH alias or saved forward]:alias' \
              '--local[local bind/port]:local' \
              '--remote[remote host/port]:remote' \
              '--through[jump alias]:alias' \
              '--background[run detached]' \
              '--print[print command]' \
              '--direct[disable supervisor]' \
              '--state-dir[override state directory]:path:_files'
          fi
          ;;
        proxy)
          if [[ $words[3] == saved ]]; then
            _arguments \
              '3:subcommand:(list show save edit delete rename)' \
              '--state-dir[override state directory]:path:_files' \
              '--config[read SSH config]:path:_files' \
              '--select[SSH alias]:alias' \
              '--bind[listener bind address]:bind' \
              '--port[listener port]:port' \
              '--description[description]:text' \
              '--json[emit JSON]' \
              '--yes[skip confirmation]'
          else
            _arguments \
              '2:subcommand:(list status stop saved)' \
              '--select[SSH alias or saved proxy]:alias' \
              '--bind[listener bind address]:bind' \
              '--port[listener port]:port' \
              '--background[run detached]' \
              '--print[print command]' \
              '--direct[disable supervisor]' \
              '--state-dir[override state directory]:path:_files'
          fi
          ;;
        check)
          _arguments \
            '--json[emit JSON]' \
            '--config[read SSH config]:path:_files' \
            '--state-dir[override state directory]:path:_files' \
            '--ssh-binary[SSH binary]:path:_files' \
            '--timeout[SSH timeout]:duration' \
            '--icmp-timeout[ICMP timeout]:duration' \
            '--no-icmp[skip ICMP ping]' \
            '--filter[filter aliases]:substring' \
            '--user[filter aliases by user]:user' \
            '--all[include pattern aliases]' \
            '--saved-forward[check saved forward]:name' \
            '--saved-forwards[check all saved forwards]'
          ;;
        session)
          _arguments \
            '2:subcommand:(list map show stop-all prune)' \
            '--json[emit JSON]' \
            '--all[include exited sessions]' \
            '--state-dir[override state directory]:path:_files' \
            '--older-than[prune records older than duration]:duration' \
            '--dry-run[preview prune]'
          ;;
        *)
          _arguments '--help[show help]'
          ;;
      esac
      ;;
  esac
}

_ssherpa "$@"
