#!/bin/bash
# SPDX-License-Identifier: Apache-2.0
# Copyright(c) 2026 Wind River Systems, Inc.
#
# Prints the requests that failed on the system API (sysinv), once or in the
# background.
#
# The deployment manager records a Warning event for each request that failed
# on the system API, whatever the resource being reconciled. The events are on
# the deployment manager deployment, so they are all in its namespace, and
# survive the pod being replaced by the unlock. Their message starts with the
# kind and name of the resource. The reason is one of:
#   SystemAPIRejected     rejected (4xx), the configuration must be corrected
#   SystemAPITransient    no response, request timeout or rate limiting
#   SystemAPIServerError  failed on the server (5xx)
# Kubernetes folds identical events into one, so a request that fails on every
# retry is printed once, with the number of times it failed.
#
# A deployment manager that does not record these events (an older release)
# only logs its failures. If no event is found, its pod log is searched for the
# errors instead, as the playbook always did: the ERROR lines, except the ones
# about validation, waiting and reconciler errors, one per distinct (10th, last)
# field. They are printed as logged, with no reason or count.
#
# Usage:
#   dm-monitor-errors.sh --watch <delay> <interval> <logfile> <namespace> \
#       [<since>]
#
# <namespace> is the namespace of the deployment manager. If <since> is
# given, a UTC time formatted as "date -u +%Y-%m-%dT%H:%M:%SZ", only the
# failures seen since then are printed.
#
# Without --watch, prints one line per failure, oldest first:
#   <last seen> <count> <reason> <resource>: <message>
# or, for the errors found in the pod log, the log line.
# Exits with a non-zero status if neither the events nor the pod log can be
# read.
#
# With --watch, collects the failures in the background instead: a playbook
# task that blocks (e.g. waiting for the unlock reboot) cannot report them
# while it runs, so roles/common/monitor-errors starts the script detached
# before such a task (tasks/start.yaml), and reports what was collected once
# the task is over (tasks/stop.yaml). No failure is reported during the first
# <delay> seconds, e.g. sysinv rejecting the unlock while the configuration is
# being applied is expected. The failures are then checked every <interval>
# seconds, and a line is appended to <logfile> whenever they change. The
# process id is written to <logfile>.pid. The collector is killed by a reboot
# of the host, by the playbook, or by the ansible async timeout, whichever
# comes first. Only the events and the log are read, so the collector never
# interferes with the deployment.

set -u -o pipefail

watch=false
if [ "${1:-}" = "--watch" ]; then
    watch=true
    delay=${2}
    interval=${3}
    logfile=${4}
    shift 4
fi

namespace=${1}
since=${2:-}

export KUBECONFIG=${KUBECONFIG:-/etc/kubernetes/admin.conf}

readonly DEPLOYMENT="deployments/platform-deployment-manager"
readonly CONTAINER="manager"
readonly FIELDS="TIME:.lastTimestamp,COUNT:.count,REASON:.reason,MESSAGE:.message"

# Prints the failures recorded as events. kubectl cannot select several
# reasons at once, hence the filter on the REASON column. The timestamps have
# the format of <since>, so they compare as strings.
event_failures() {
    kubectl -n "${namespace}" get events \
        --field-selector type=Warning,involvedObject.kind=Deployment \
        --sort-by=.lastTimestamp --no-headers -o custom-columns="${FIELDS}" |
        awk -v since="${since}" '$3 ~ /^SystemAPI/ && $1 >= since'
}

# Prints the errors found in the pod log, as they were logged.
log_failures() {
    local logs
    # --since-time keeps the errors of a previous run out of the report.
    logs=$(kubectl -n "${namespace}" logs "${DEPLOYMENT}" "${CONTAINER}" \
        ${since:+--since-time="${since}"} 2>&1) || {
        echo "${logs}" >&2
        return 1
    }

    awk '/ERROR/ &&
        !/validation/ &&
        !/waiting/ &&
        !/Reconciler error/ &&
        !(seen[$10, $NF]++)' <<<"${logs}"
}

# Prints the failures, from the events or else from the pod log.
get_failures() {
    local events events_failed
    events=$(event_failures)
    events_failed=$?
    if [ "${events_failed}" -eq 0 ] && [ -n "${events}" ]; then
        echo "${events}"
        return 0
    fi
    # Nothing was recorded as an event, or the events cannot be read: this is
    # expected with an older deployment manager. Look in its log instead.

    if log_failures; then
        return 0
    fi

    # The log is not available when the pods are drained by the unlock. That
    # is not an error if the events could be read, they were just empty.
    [ "${events_failed}" -eq 0 ] && return 0
    echo "neither the events nor the log of the deployment manager can be read" >&2
    return 1
}

# Appends a line to <logfile> whenever the failures change, until killed.
watch_failures() {
    local pidfile start previous elapsed output status failure

    # Start fresh: a log left behind by a previous (failed) run must not be
    # reported as the result of this one.
    : >"${logfile}" || exit 1

    # Used by the playbook to stop the collector. Matching the command line
    # instead (pkill -f) would also match any other process that happens to
    # mention this script, including the playbook invoking it.
    pidfile="${logfile}.pid"
    echo $$ >"${pidfile}"
    # shellcheck disable=SC2064 # expand now: pidfile is local
    trap "rm -f '${pidfile}'" EXIT

    start=$(date +%s)
    previous=""

    # Nothing is written until there is something to report, so that a task
    # that completes right away leaves an empty log and nothing to show.
    while true; do
        sleep "${interval}"

        elapsed=$(($(date +%s) - start))
        if [ "${elapsed}" -lt "${delay}" ]; then
            continue
        fi

        if ! output=$(get_failures 2>&1); then
            # Expected whenever the kube-apiserver is unreachable. Keep polling.
            status="the system API failures are not available: $(head -1 <<<"${output}")"
        elif [ -z "${output}" ]; then
            status="the deployment manager reported no system API failure"
        else
            status="requests that failed on the system API"
            status="${status} (reason, resource, message):"
            # Without the time and count of each failure, which change on
            # every retry: only a new failure is worth reporting.
            while read -r _ _ failure; do
                status="${status}"$'\n'"    ${failure}"
            done <<<"${output}"
        fi

        # Only report changes: the status is otherwise repeated every interval
        # until the collector is stopped.
        if [ "${status}" != "${previous}" ]; then
            printf '%s  %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
                "after ${elapsed}s: ${status}" >>"${logfile}"
            previous="${status}"
        fi
    done
}

if "${watch}"; then
    watch_failures
else
    get_failures
fi
