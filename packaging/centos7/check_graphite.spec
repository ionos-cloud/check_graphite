Name:    check_graphite
Version: 0.0_2019010713
Release: 1%{?dist}
Summary: Check metrics from graphite data source.

Group:   Application/System
License: 1&1 Internet SE

# Bitbucket doesn't provide a rpm compatible download link, so this has to be
# done manually. Last time I got this from:
# https://bitbucket.1and1.org/rest/api/latest/projects/ITODNS/repos/%{name}/archive?at=refs%2Ftags%2F%{version}&format=tar.gz
Source0: %{name}-%{version}.tar.gz

%description
check_graphite runs a query against a graphite host and checks if a limit was broken in a given interval.

%prep
%setup -q #unpack tarball

%build

%install
cp -rfa * %{buildroot}

%files
%{_bindir}/*
%{_sysconfdir}/*
