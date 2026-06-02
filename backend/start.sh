#!/bin/bash
cd /home/sun/workspace/EHomeSystem/backend
export EHOME_DB_HOST=localhost
export EHOME_DB_PORT=9432
export EHOME_DB_USER=homestation
export EHOME_DB_PASSWORD=*** EHOME_DB_NAME=ehome
export EHOME_DB_SSLMODE=disable
exec ./ehome-server
