#!/usr/bin/env sh

DATADIR=${DATADIR:-/data}
BINDHOST=${BINDHOST:-0.0.0.0}
PORT=${PORT:-8080}
CONFIGDIR=${CONFIGDIR:-/config}
CONFIG_FILE="${CONFIG_FILE:-${CONFIGDIR}/arbiter.yaml}"
DEFAULT_ARGS="-bind ${BINDHOST} -port ${PORT} -config ${CONFIG_FILE}"

abort() {
    showHelp

    echo "[CONTAINER] ERROR: $@" 1>&2

    exit 1
}
log () {
    echo "[CONTAINER] $@"
}
showHelp() {
    log
    log "Container args: [command] [options]"
    log
    log "Commands:"
    log "  arbiter                             Start the arbiter server (default)"
    log "  catalog-convert, catalog, convert   Run the catalog convert tool. ${CONFIG_FILE} will be used"
    log "                                      as the configuration file. Other arguments will be passed to the"
    log "                                      catalog convert tool."
    log "  sh, /bin/sh                         Start a shell"
    log "  -h                                  Show this help message"
    log
    log "If no command is provided, the arbiter server will be started with default arguments."
    log
    log 
    log "Recommended way of customizing instead of using command-line arguments, is setting"
    log "the following environment variables:"
    log "  PORT        - Port to bind the arbiter server (default: 8080)"
    log "  BINDHOST    - Host to bind the arbiter server (default: 0.0.0.0)"
    log "  CONFIGDIR   - Directory where the configuration file is located (default: /config)"
    log "  CONFIGFILE  - Path to the configuration file (default: \${CONFIGDIR}/arbiter.yaml)"
    log "  DATADIR     - Directory where the data is stored (default: /data)"
    log "                still need to use this in the config file!"
    log
    log "Default arguments that would be passed to arbiter with the current environment:"
    log "  ${DEFAULT_ARGS}"
    log
}
checkConfig() {
    if [ ! -f "${CONFIG_FILE}" ]; then
        showHelp

        log
        log "######################" ERROR "######################"
        log
        log "Configuration file ${CONFIG_FILE} not found."
        log "Please create it or use the default configuration."
        log
        log "Example configuration will be copied to the ${DATADIR}/examples folder if it does not exist."
        log "Mount the ${DATADIR} as a writeable volume for $(id -u):$(id -g) in your container to easily get the example configuration files."
        log

        exit 1
    fi
}


# Check if the DATA folder contains an "examples" folder, if not copy the default examples from the image
if [ ! -d "${DATADIR}/examples" ]; then
    log "Copying default examples to ${DATADIR}/examples"
    mkdir -p "${DATADIR}/examples" || abort "Failed to create ${DATADIR}/examples"
    cp -r /app/examples/* "${DATADIR}/examples" || abort "Failed to copy default examples to ${DATADIR}/examples"
fi


case "$1" in
    catalog-convert|catalog|convert)
        # Catalog convert
        shift
        checkConfig
        exec /app/catalog-convert -config "${CONFIG_FILE}" "$@"
        ;;
    sh|/bin/sh)
        # Shell
        shift
        exec /bin/sh "$@"
        ;;
    -h)
        # Help
        showHelp
        ;;
    *)
        shift
        checkConfig
        exec /app/arbiter ${DEFAULT_ARGS}
        ;;
esac
