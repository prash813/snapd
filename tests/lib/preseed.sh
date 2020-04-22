#!/bin/bash

# mount ubuntu cloud image through qemu-nbd and mount
# critical virtual filesystems (such as proc) under
# the root of mounted image.
# The path of the image needs to be absolute as a systemd service
# gets created for qemu-nbd.
mount_ubuntu_image() {
    local CLOUD_IMAGE=$1
    local IMAGE_MOUNTPOINT=$2

    if ! lsmod | grep nbd; then
        modprobe nbd
    fi

    # Run qemu-ndb as a service, so that it does not interact with ssh
    # stdin/stdout it would otherwise inherit from the spread session.
    systemd-run --system --service-type=forking --unit=qemu-ndb-preseed.service "$(command -v qemu-nbd)" --fork -c /dev/nbd0 "$CLOUD_IMAGE"
    # nbd0p1 may take a short while to become available
    retry-tool -n 5 --wait 1 test -e /dev/nbd0p1
    mount /dev/nbd0p1 "$IMAGE_MOUNTPOINT"
    mount -t proc /proc "$IMAGE_MOUNTPOINT/proc"
    mount -t sysfs sysfs "$IMAGE_MOUNTPOINT/sys"
    mount -t devtmpfs udev "$IMAGE_MOUNTPOINT/dev"
    mount -t securityfs securityfs "$IMAGE_MOUNTPOINT/sys/kernel/security"
}

umount_ubuntu_image() {
    local IMAGE_MOUNTPOINT=$1

    for fs in proc dev sys/kernel/security sys; do
        umount "$IMAGE_MOUNTPOINT/$fs"
    done
    umount "$IMAGE_MOUNTPOINT"
    rmdir "$IMAGE_MOUNTPOINT"

    # qemu-nbd -d may sporadically fail when removing the device,
    # reporting it's still in use.
    retry-tool -n 5 --wait 1 qemu-nbd -d /dev/nbd0
}

# XXX inject new snapd into the core image in seed/snaps of the cloud image
# and make core unasserted.
# this will go away once snapd on the core is new enough to support
# pre-seeding.
setup_preseeding() {
    local IMAGE_MOUNTPOINT=$1
    local SNAP_IMAGE

    #shellcheck source=tests/lib/snaps.sh
    . "$TESTSLIB"/snaps.sh

    for name in core snapd; do
        SNAP_IMAGE=$(find "$IMAGE_MOUNTPOINT/var/lib/snapd/seed/snaps/" -name "${name}_*.snap")
        if [ -e "$SNAP_IMAGE" ]; then
            unsquashfs "$SNAP_IMAGE"
            cp /usr/lib/snapd/snapd squashfs-root/usr/lib/snapd/snapd
            # XXX to satisfy version check; this will go away once pre-seeding
            # is available in 2.44
            echo "VERSION=2.44.0" > squashfs-root/usr/lib/snapd/info
            rm "$SNAP_IMAGE"
            mksnap_fast squashfs-root "$SNAP_IMAGE"
            sed -i "$IMAGE_MOUNTPOINT/var/lib/snapd/seed/seed.yaml" -E -e "s/^(\\s+)name: $name/\\1name: $name\\n\\1unasserted: true/"
        fi
    done
}
