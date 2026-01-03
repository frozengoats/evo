# evo
evo is a postgres compatible database migration tool, designed to operate as a standalone, dependency-free binary.

## features
- create database
- roll forward migrations
- concurrent access mitigation using postgres/cockroachdb compatible locking strategy

## cli usage
```
evo <directory>
```
directory contents will be treated as go templates and processed in alphabetical order.   the environment will be supplied to each migrator template for rendering, prior to execution.  each template must contain only valid SQL.  each migrator will be transacted, unless the file contains the suffix `_notrans.sql`, in which case it will not be.  in such cases, the sql is assumed to be non-transactable.  files must contain the extension `.sql` or they will not be processed.

## schema setup
evo takes the following environment variables, all are mandatory:

| name    | description |
| -------- | ------- |
| EVO_DB_HOST | database hostname in the form of `<host>:<port>` |
| EVO_DB_DATABASE | the name of the database to be created and/or migrated |
| EVO_DB_ADMIN_USERNAME | the administrative username |
| EVO_DB_ADMIN_PASSWORD | the administrative password |
| EVO_DB_USERNAME | the non-administrative username |
| EVO_DB_PASSWORD | the non-administrative password |
| EVO_AUTO_UPDATE_PASSWORD | when set to `1`, user password will be synced to the database if it differs in the environment variable, so long as it is non-empty |

evo will perform a few operations on each invocation, in the following order:
- create a session with the administrative user account
- take out an advisory lock, namespaced to the specified database, to ensure atomicity
- ensure that the database exists (or create it if it doesn't)
- ensure that the non-admin user exists (or is created if it doesn't, and grant schema rights to the database)
- test the non-admin user password matches that which is specified in the environment and correct it if it does not match


## docker container usage
```
docker run --rm -v /home/user/migrations:/migrations \
  -e EVO_DB_HOST=hostname:5432 \
  -e EVO_DB_DATABASE=mydbname \
  -e EVO_DB_ADMIN_USERNAME=adminuser \
  -e EVO_DB_ADMIN_PASSWORD=adminpassword \
  -e EVO_DB_USERNAME=username \
  -e EVO_DB_PASSWORD=password \
  -e EVO_AUTO_UPDATE_PASSWORD=1 \
   frozengoats/evo
```