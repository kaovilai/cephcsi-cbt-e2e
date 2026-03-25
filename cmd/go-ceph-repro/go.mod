module go-ceph-repro

go 1.24.0

require github.com/ceph/go-ceph v0.32.0

require golang.org/x/sys v0.38.0 // indirect

// Use the exact same Red Hat fork version as CephCSI release-4.21
replace github.com/ceph/go-ceph => github.com/red-hat-storage/go-ceph v0.32.1-0.20260109062642-357605f36918
