# bash completion for ssherpa

_ssherpa()
{
    local cur prev words cword
    _init_completion || return

    local commands="add edit jump proxy forward send receive recv check incoming authkeys theme session list show version help"
    local connect_flags="--json --all --filter --user --config --print --exec --select --ssh-binary --supervise --direct --state-dir --latency-warn --latency-disconnect --composer-key --no-composer --overlay-key --no-record --record-max-bytes --no-kitty --no-color --theme-file --help"

    if [[ $cword -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "$commands $connect_flags" -- "$cur") )
        return
    fi

    case "${words[1]}" in
        add)
            COMPREPLY=( $(compgen -W "--alias --host --user --port --identity --identities-only --config --dry-run --yes" -- "$cur") )
            return
            ;;
        edit)
            case "${words[2]}" in
                set)
                    COMPREPLY=( $(compgen -W "--host --user --clear-user --port --clear-port --identity --clear-identity --identities-only --no-identities-only --config --dry-run --yes" -- "$cur") )
                    return
                    ;;
                delete|remove)
                    COMPREPLY=( $(compgen -W "--all-sources --delete-patterns --state-dir --config --dry-run --yes" -- "$cur") )
                    return
                    ;;
                delete-all)
                    COMPREPLY=( $(compgen -W "--confirm --dry-run --all --filter --user --config --state-dir --yes" -- "$cur") )
                    return
                    ;;
                *)
                    COMPREPLY=( $(compgen -W "set delete delete-all" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        jump)
            COMPREPLY=( $(compgen -W "--dest --hop --print --exec --direct --supervise --state-dir --ssh-binary --no-color --theme-file" -- "$cur") )
            return
            ;;
        forward)
            case "${words[2]}" in
                saved)
                    case "${words[3]}" in
                        save|edit)
                            COMPREPLY=( $(compgen -W "--state-dir --config --select --local --remote --through --clear-through --description --clear-description --yes" -- "$cur") )
                            return
                            ;;
                        list|show|delete|rename)
                            COMPREPLY=( $(compgen -W "--state-dir --json --yes" -- "$cur") )
                            return
                            ;;
                        *)
                            COMPREPLY=( $(compgen -W "list show save edit delete rename" -- "$cur") )
                            return
                            ;;
                    esac
                    ;;
                list|status|stop)
                    COMPREPLY=( $(compgen -W "--json --state-dir --yes" -- "$cur") )
                    return
                    ;;
                *)
                    COMPREPLY=( $(compgen -W "list status stop saved --select --local --remote --through --background --print --exec --direct --state-dir --ssh-binary --reconnect-max --no-reconnect --reconnect-backoff --reconnect-max-backoff" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        proxy)
            case "${words[2]}" in
                saved)
                    case "${words[3]}" in
                        save|edit)
                            COMPREPLY=( $(compgen -W "--state-dir --config --select --bind --port --description --clear-description --yes" -- "$cur") )
                            return
                            ;;
                        list|show|delete|rename)
                            COMPREPLY=( $(compgen -W "--state-dir --json --yes" -- "$cur") )
                            return
                            ;;
                        *)
                            COMPREPLY=( $(compgen -W "list show save edit delete rename" -- "$cur") )
                            return
                            ;;
                    esac
                    ;;
                list|status|stop)
                    COMPREPLY=( $(compgen -W "--json --state-dir --yes" -- "$cur") )
                    return
                    ;;
                *)
                    COMPREPLY=( $(compgen -W "list status stop saved --select --bind --port --background --print --exec --direct --state-dir --ssh-binary" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        check)
            COMPREPLY=( $(compgen -W "--json --config --state-dir --ssh-binary --timeout --icmp-timeout --no-icmp --filter --user --all --saved-forward --saved-forwards" -- "$cur") )
            return
            ;;
        incoming)
            case "${words[2]}" in
                list)
                    COMPREPLY=( $(compgen -W "--json --runtime-dir" -- "$cur") )
                    return
                    ;;
                mark)
                    COMPREPLY=( $(compgen -W "--watch-parent --quiet --runtime-dir" -- "$cur") )
                    return
                    ;;
                hook)
                    COMPREPLY=( $(compgen -W "--shell sh bash zsh fish" -- "$cur") )
                    return
                    ;;
                *)
                    COMPREPLY=( $(compgen -W "list mark hook --json --runtime-dir --watch-parent --quiet --shell" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        send)
            COMPREPLY=( $(compgen -W "--select --remote --config --sftp-binary --force --print --filter --user --all --no-color --theme-file" -- "$cur") )
            return
            ;;
        receive|recv)
            COMPREPLY=( $(compgen -W "--select --local --config --sftp-binary --force --print --filter --user --all --no-color --theme-file" -- "$cur") )
            return
            ;;
        authkeys)
            case "${words[2]}" in
                list)
                    COMPREPLY=( $(compgen -W "--json --path" -- "$cur") )
                    return
                    ;;
                add)
                    COMPREPLY=( $(compgen -W "--key --key-file --path --ssh-keygen --yes" -- "$cur") )
                    return
                    ;;
                merge)
                    COMPREPLY=( $(compgen -W "--from-dir --path --ssh-keygen --dry-run" -- "$cur") )
                    return
                    ;;
                replace)
                    COMPREPLY=( $(compgen -W "--from-dir --path --ssh-keygen --yes" -- "$cur") )
                    return
                    ;;
                delete|remove)
                    COMPREPLY=( $(compgen -W "--fingerprint --all-matching --path --ssh-keygen --yes" -- "$cur") )
                    return
                    ;;
                *)
                    COMPREPLY=( $(compgen -W "list add merge replace delete" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        theme)
            COMPREPLY=( $(compgen -W "--theme-file --no-color" -- "$cur") )
            return
            ;;
        session)
            case "${words[2]}" in
                bundle)
                    case "${words[3]}" in
                        export|import)
                            COMPREPLY=( $(compgen -W "--output --json --state-dir" -- "$cur") )
                            return
                            ;;
                        *)
                            COMPREPLY=( $(compgen -W "export import" -- "$cur") )
                            return
                            ;;
                    esac
                    ;;
                log)
                    COMPREPLY=( $(compgen -W "--raw --tail --follow --state-dir" -- "$cur") )
                    return
                    ;;
                replay)
                    COMPREPLY=( $(compgen -W "--speed --no-delay --state-dir" -- "$cur") )
                    return
                    ;;
                grep)
                    COMPREPLY=( $(compgen -W "--ignore-case --json --state-dir" -- "$cur") )
                    return
                    ;;
                export)
                    COMPREPLY=( $(compgen -W "--format --output --state-dir text asciicast" -- "$cur") )
                    return
                    ;;
                prune)
                    COMPREPLY=( $(compgen -W "--older-than --dry-run --state-dir" -- "$cur") )
                    return
                    ;;
                map)
                    COMPREPLY=( $(compgen -W "--json --all --state-dir" -- "$cur") )
                    return
                    ;;
                list|show|identity|stop-all)
                    COMPREPLY=( $(compgen -W "--json --state-dir" -- "$cur") )
                    return
                    ;;
                browse|transcripts)
                    COMPREPLY=( $(compgen -W "--state-dir" -- "$cur") )
                    return
                    ;;
                *)
                    COMPREPLY=( $(compgen -W "list map show log replay grep export bundle identity browse transcripts stop-all prune" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        list)
            COMPREPLY=( $(compgen -W "--json --all --filter --user --config" -- "$cur") )
            return
            ;;
        show)
            COMPREPLY=( $(compgen -W "--json --config" -- "$cur") )
            return
            ;;
        help)
            COMPREPLY=( $(compgen -W "connect $commands" -- "$cur") )
            return
            ;;
        *)
            COMPREPLY=( $(compgen -W "$connect_flags" -- "$cur") )
            return
            ;;
    esac
}

complete -F _ssherpa ssherpa
