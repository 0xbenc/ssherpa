#compdef ssherpa

_ssherpa() {
  local -a commands
  commands=(
    'add:Add or update an SSH alias'
    'edit:Edit or delete SSH aliases and saved forwards'
    'jump:Connect through ProxyJump hops'
    'proxy:Start a local SOCKS proxy'
    'forward:Open and manage port-forward tunnels'
    'send:Send a local file with SFTP'
    'receive:Receive a remote file with SFTP'
    'recv:Receive a remote file with SFTP (alias)'
    'check:Test SSH aliases and saved forwards'
    'incoming:Inspect incoming SSH sessions'
    'authkeys:Manage authorized_keys'
    'theme:Build and save the terminal UI color schema'
    'session:Inspect supervised session records'
    'list:List SSH aliases'
    'show:Show one SSH alias'
    'version:Print build version'
    'help:Show help'
  )

  _arguments -C \
    '--json[emit JSON output]' \
    '--all[include wildcard and negated Host patterns]' \
    '--filter[filter aliases by substring]:substring' \
    '--user[filter aliases by parsed user]:user' \
    '--config[read this SSH config root]:path:_files' \
    '--print[print the SSH command instead of running it]' \
    '--exec[run the SSH command]' \
    '--select[select an alias non-interactively]:alias' \
    '--ssh-binary[use this SSH binary]:path:_files' \
    '--supervise[run under supervised PTY]' \
    '--direct[use the direct runner without session overlay/state]' \
    '--state-dir[override session state directory]:path:_files' \
    '--latency-warn[warn above latency threshold]:duration' \
    '--latency-disconnect[disconnect after sustained unhealthy probes]:duration' \
    '--composer-key[queued-input composer key]:key' \
    '--no-composer[disable queued-input composer]' \
    '--overlay-key[session-map overlay key]:key' \
    '--no-record[disable transcript recording]' \
    '--record-max-bytes[cap transcript size]:bytes' \
    '--no-kitty[disable Kitty SSH command detection]' \
    '--no-color[disable color styling]' \
    '--theme-file[load UI theme config]:path:_files' \
    '1:command:->command' \
    '*::arg:->args'

  case $state in
    command)
      _describe -t commands 'ssherpa commands' commands
      ;;
    args)
      case $words[1] in
        add)
          _arguments \
            '--alias[alias name]:name' \
            '--host[host name]:host' \
            '--user[user]:user' \
            '--port[port]:port' \
            '--identity[identity file]:path:_files' \
            '--identities-only[set IdentitiesOnly yes]' \
            '--config[read SSH config]:path:_files' \
            '--dry-run[preview without writing]' \
            '--yes[skip confirmation]'
          ;;
        edit)
          _arguments \
            '1:subcommand:(set delete delete-all)' \
            '2:alias' \
            '--host[host name]:host' \
            '--user[user]:user' \
            '--clear-user[clear user]' \
            '--port[port]:port' \
            '--clear-port[clear port]' \
            '--identity[identity file]:path:_files' \
            '--clear-identity[clear identity]' \
            '--identities-only[set IdentitiesOnly yes]' \
            '--no-identities-only[unset IdentitiesOnly]' \
            '--all-sources[delete from all config sources]' \
            '--delete-patterns[allow deleting pattern hosts]' \
            '--confirm[type-to-confirm phrase]:phrase' \
            '--state-dir[override state directory]:path:_files' \
            '--config[read SSH config]:path:_files' \
            '--dry-run[preview without writing]' \
            '--yes[skip confirmation]'
          ;;
        jump)
          _arguments \
            '--dest[destination alias]:alias' \
            '--hop[hop alias]:alias' \
            '--print[print command]' \
            '--direct[disable supervisor]' \
            '--state-dir[override state directory]:path:_files' \
            '--ssh-binary[use this SSH binary]:path:_files'
          ;;
        forward)
          if [[ $words[2] == saved ]]; then
            _arguments \
              '2:subcommand:(list show save edit delete rename)' \
              '--state-dir[override state directory]:path:_files' \
              '--config[read SSH config]:path:_files' \
              '--select[SSH alias]:alias' \
              '--local[local bind/port]:local' \
              '--remote[remote host/port]:remote' \
              '--through[jump alias]:alias' \
              '--clear-through[clear jump alias]' \
              '--description[description]:text' \
              '--clear-description[clear description]' \
              '--json[emit JSON]' \
              '--yes[skip confirmation]'
          else
            _arguments \
              '1:subcommand:(list status stop saved)' \
              '--select[SSH alias or saved forward]:alias' \
              '--local[local bind/port]:local' \
              '--remote[remote host/port]:remote' \
              '--through[jump alias]:alias' \
              '--background[run detached]' \
              '--print[print command]' \
              '--direct[disable supervisor]' \
              '--reconnect-max[max reconnect attempts, 0 = unlimited]:count' \
              '--no-reconnect[disable reconnect]' \
              '--reconnect-backoff[initial reconnect backoff]:duration' \
              '--reconnect-max-backoff[max reconnect backoff]:duration' \
              '--json[emit JSON]' \
              '--state-dir[override state directory]:path:_files'
          fi
          ;;
        proxy)
          if [[ $words[2] == saved ]]; then
            _arguments \
              '2:subcommand:(list show save edit delete rename)' \
              '--state-dir[override state directory]:path:_files' \
              '--config[read SSH config]:path:_files' \
              '--select[SSH alias]:alias' \
              '--bind[listener bind address]:bind' \
              '--port[listener port]:port' \
              '--description[description]:text' \
              '--clear-description[clear description]' \
              '--json[emit JSON]' \
              '--yes[skip confirmation]'
          else
            _arguments \
              '1:subcommand:(list status stop saved)' \
              '--select[SSH alias or saved proxy]:alias' \
              '--bind[listener bind address]:bind' \
              '--port[listener port]:port' \
              '--background[run detached]' \
              '--print[print command]' \
              '--direct[disable supervisor]' \
              '--json[emit JSON]' \
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
        incoming)
          _arguments \
            '1:subcommand:(list mark hook)' \
            '--json[emit JSON]' \
            '--runtime-dir[override incoming marker directory]:path:_files' \
            '--watch-parent[watch parent process id]:pid' \
            '--quiet[suppress marker output]' \
            '--shell[shell hook type]:shell:(sh bash zsh fish)'
          ;;
        send)
          _arguments \
            '1:local file:_files' \
            '--select[SSH alias]:alias' \
            '--remote[remote destination path]:remote path' \
            '--config[read SSH config]:path:_files' \
            '--sftp-binary[SFTP binary]:path:_files' \
            '--force[overwrite existing destination]' \
            '--print[print command]' \
            '--filter[filter aliases]:substring' \
            '--user[filter aliases by user]:user' \
            '--all[include pattern aliases]' \
            '--no-color[disable color styling]' \
            '--theme-file[load UI theme config]:path:_files'
          ;;
        receive|recv)
          _arguments \
            '1:remote file:remote path' \
            '--select[SSH alias]:alias' \
            '--local[local destination path]:path:_files' \
            '--config[read SSH config]:path:_files' \
            '--sftp-binary[SFTP binary]:path:_files' \
            '--force[overwrite existing destination]' \
            '--print[print command]' \
            '--filter[filter aliases]:substring' \
            '--user[filter aliases by user]:user' \
            '--all[include pattern aliases]' \
            '--no-color[disable color styling]' \
            '--theme-file[load UI theme config]:path:_files'
          ;;
        authkeys)
          _arguments \
            '1:subcommand:(list add merge replace delete)' \
            '--json[emit JSON]' \
            '--path[authorized_keys path]:path:_files' \
            '--key[public key line]:key' \
            '--key-file[public key file]:path:_files' \
            '--from-dir[directory of public keys]:path:_files' \
            '--fingerprint[key fingerprint]:fingerprint' \
            '--all-matching[delete every entry sharing a fingerprint]' \
            '--ssh-keygen[use this ssh-keygen binary]:path:_files' \
            '--dry-run[preview without writing]' \
            '--yes[skip confirmation]'
          ;;
        theme)
          _arguments \
            '--theme-file[edit this theme config path]:path:_files' \
            '--no-color[disable color styling]'
          ;;
        authkeys)
          if [[ $words[3] == seed ]]; then
            _arguments \
              '3:subcommand:(seed)' \
              '--json[emit JSON]' \
              '--all[include pattern aliases]' \
              '--filter[filter aliases]:substring' \
              '--user[filter aliases by user]:user' \
              '--config[read SSH config]:path:_files' \
              '--key[public key line]:key' \
              '--key-file[public key file]:path:_files' \
              '--from-dir[key directory]:path:_files' \
              '--target[remote SSH alias]:alias' \
              '--hop[target route TARGET=HOP[,HOP...]]:route' \
              '--dry-run[preview without writing]' \
              '--yes[skip confirmation]' \
              '--ssh-keygen[ssh-keygen binary]:path:_files' \
              '--ssh-binary[SSH binary]:path:_files' \
              '--timeout[SSH timeout]:duration'
          elif [[ $words[3] == revoke || $words[3] == unseed ]]; then
            _arguments \
              '3:subcommand:(revoke unseed)' \
              '--json[emit JSON]' \
              '--all[include pattern aliases]' \
              '--filter[filter aliases]:substring' \
              '--user[filter aliases by user]:user' \
              '--config[read SSH config]:path:_files' \
              '--key[public key line]:key' \
              '--key-file[public key file]:path:_files' \
              '--target[remote SSH alias]:alias' \
              '--hop[target route TARGET=HOP[,HOP...]]:route' \
              '--dry-run[preview without writing]' \
              '--yes[skip confirmation]' \
              '--ssh-keygen[ssh-keygen binary]:path:_files' \
              '--ssh-binary[SSH binary]:path:_files' \
              '--timeout[SSH timeout]:duration'
          else
            _arguments \
              '2:subcommand:(list add merge replace delete seed revoke unseed)' \
              '--json[emit JSON]' \
              '--path[authorized_keys path]:path:_files' \
              '--key[public key line]:key' \
              '--key-file[public key file]:path:_files' \
              '--from-dir[key directory]:path:_files' \
              '--target[remote SSH alias]:alias' \
              '--hop[target route TARGET=HOP[,HOP...]]:route' \
              '--fingerprint[key fingerprint]:fingerprint' \
              '--dry-run[preview without writing]' \
              '--yes[skip confirmation]' \
              '--ssh-keygen[ssh-keygen binary]:path:_files' \
              '--ssh-binary[SSH binary]:path:_files' \
              '--timeout[SSH timeout]:duration'
          fi
          ;;
        session)
          if [[ $words[2] == bundle ]]; then
            _arguments \
              '2:subcommand:(export import)' \
              '--output[bundle output path]:path:_files' \
              '--json[emit JSON]' \
              '--state-dir[override state directory]:path:_files'
          else
            _arguments \
              '1:subcommand:(list map show log replay grep export bundle identity browse transcripts stop-all prune)' \
              '--json[emit JSON]' \
              '--all[include exited sessions]' \
              '--state-dir[override state directory]:path:_files' \
              '--raw[emit raw transcript bytes]' \
              '--tail[show only the last N lines]:lines' \
              '--follow[follow a live transcript]' \
              '--speed[replay speed multiplier]:speed' \
              '--no-delay[replay without frame delays]' \
              '--ignore-case[case-insensitive grep]' \
              '--format[export format]:format:(text asciicast)' \
              '--output[export output path]:path:_files' \
              '--older-than[prune records older than duration]:duration' \
              '--dry-run[preview prune]'
          fi
          ;;
        list)
          _arguments \
            '--json[emit JSON]' \
            '--all[include pattern aliases]' \
            '--filter[filter aliases]:substring' \
            '--user[filter aliases by user]:user' \
            '--config[read SSH config]:path:_files'
          ;;
        show)
          _arguments \
            '1:alias' \
            '--json[emit JSON]' \
            '--config[read SSH config]:path:_files'
          ;;
        help)
          _arguments \
            '1:topic:(connect add edit jump proxy forward send receive recv check incoming authkeys theme session list show version help)'
          ;;
        *)
          _arguments '--help[show help]'
          ;;
      esac
      ;;
  esac
}

_ssherpa "$@"
