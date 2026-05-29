# bash completion for ssherpa

_ssherpa()
{
    local cur prev words cword
    _init_completion || return

    local commands="add edit jump proxy forward check authkeys theme session list show version help"
    local global_flags="--json --all --filter --user --config --state-dir --ssh-binary --no-color --theme-file --help"

    case "${words[1]}" in
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
                    COMPREPLY=( $(compgen -W "list status stop saved --select --local --remote --through --background --print --direct --state-dir --reconnect-max --no-reconnect" -- "$cur") )
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
                    COMPREPLY=( $(compgen -W "list status stop saved --select --bind --port --background --print --direct --state-dir" -- "$cur") )
                    return
                    ;;
            esac
            ;;
        check)
            COMPREPLY=( $(compgen -W "--json --config --state-dir --ssh-binary --timeout --icmp-timeout --no-icmp --filter --user --all --saved-forward --saved-forwards" -- "$cur") )
            return
            ;;
        authkeys)
            COMPREPLY=( $(compgen -W "list add merge replace delete --json --path --key --key-file --from-dir --fingerprint --dry-run --yes" -- "$cur") )
            return
            ;;
        session)
            COMPREPLY=( $(compgen -W "list map show stop-all prune --json --all --state-dir --older-than --dry-run" -- "$cur") )
            return
            ;;
        *)
            COMPREPLY=( $(compgen -W "$commands $global_flags" -- "$cur") )
            return
            ;;
    esac
}

complete -F _ssherpa ssherpa
