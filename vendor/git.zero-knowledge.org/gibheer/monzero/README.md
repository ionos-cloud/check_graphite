monzero
=======

Monzero is a collection of tools with the purpose of running monitoring checks
and triggering notifications.

requirements
------------

runtime requirements:
* PostgreSQL >= 10.0

build requirements:
* Go >= 1.11

components
----------

The following components exist:

### moncheck

Moncheck is the daemon that runs the checks and generates notifications in the
database.
It is possible to run multiple instances of moncheck, as it uses PostgreSQL
as a coordinator through the PostgreSQL internal locking mechanism.

Moncheck uses the table `active_checks` to detect which checks to run.

### monfront

Monfront is a webfrontend to view the current state of all checks, configure
hosts, groups, checks and view current notifications.
It is possible to run multiple instances.

### monwork

Monwork is a small server that does all the maintenance work in the background.
It is responsible to cleanup the history and generate the configuration.

The configuration is generated into `active_checks` when an entry in `nodes`,
`command` or `checks` was changed (detected through the updated column).

configuration
-------------

To get the system working, first install the database. After that, create an
alarm mapping:

```
insert into mappings(name, description) values ('default', 'The default mapping');
insert into mapping_level values (1, 0, 0, 'okay', 'green');
insert into mapping_level values (1, 1, 1, 'okay', 'orange');
insert into mapping_level values (1, 2, 2, 'okay', 'red');
insert into mapping_level values (1, 3, 3, 'okay', 'gray');
```

Next is to create a notifier. This feature doesn't work 100% yet and needs some
work and may look different later:

```
insert into notifier(name) values ('default');
```

After that create a check command:

```
insert into commands(name, command, message) values ('ping', 'ping -n -c 1 {{ .ip }}', 'Ping a target');
```

This command can contain variables that are set in the check. It will be executed by moncheck and the result stored.

After that, create a node which will get the checks attached:

```
insert into nodes(name, message) values ('localhost', 'My localhost is my castle');
```

With that prepared, create the first check:

```
insert into checks(node_id, command_id, notifier_id, message, options)
values (1, 1, 1, 'This is my localhost ping check!', '{"ip": "127.0.0.1"}');
```

Now start the daemons moncheck, monfront and monwork.

monwork will transform the configured check into an active check, while moncheck
will run the actual checks. Through monfront one can view the current status.
