package fleet

import "baremetal-rollout/internal/domain"

func DefaultTemplates() map[string]domain.RAIDTemplate {
	return map[string]domain.RAIDTemplate{
		"compute-standard": {
			ID: "compute-standard", Name: "通用计算：双盘 RAID1 系统 + RAID5 数据",
			ApplicableModels: []string{"CX-6248", "MEM-9416"},
			MinDisks:         8, RequiredDiskType: "NVMe", RequiredModel: "NVME7T64-G5",
			Controller: "RAID-9560-8i", Personality: "RAID",
			Volumes: []domain.RAIDVolumeTemplate{
				{Name: "os", Level: "raid1", DiskCount: 2},
				{Name: "data", Level: "raid5", DiskCount: 6, HotSpare: 0},
			},
			Partitions: []domain.PartitionSpec{
				{Mount: "/boot/efi", SizeMB: 1024, FileSystem: "vfat"},
				{Mount: "/boot", SizeMB: 2048, FileSystem: "ext4"},
				{Mount: "/", SizeMB: 102400, FileSystem: "xfs"},
				{Mount: "/var/lib/containers", SizeMB: 204800, FileSystem: "xfs"},
				{Mount: "/data", SizeMB: 0, FileSystem: "xfs"},
			},
		},
		"storage-raid6": {
			ID: "storage-raid6", Name: "存储机型：10 盘 RAID6 + 2 热备",
			ApplicableModels: []string{"ST-3608"},
			MinDisks:         12, RequiredDiskType: "SAS", RequiredModel: "SAS16T-7K",
			Controller: "RAID-9670-24i", Personality: "RAID",
			Volumes: []domain.RAIDVolumeTemplate{
				{Name: "os", Level: "raid1", DiskCount: 2},
				{Name: "data", Level: "raid6", DiskCount: 8, HotSpare: 2},
			},
			Partitions: []domain.PartitionSpec{
				{Mount: "/boot/efi", SizeMB: 1024, FileSystem: "vfat"},
				{Mount: "/boot", SizeMB: 2048, FileSystem: "ext4"},
				{Mount: "/", SizeMB: 102400, FileSystem: "xfs"},
				{Mount: "/data", SizeMB: 0, FileSystem: "xfs"},
			},
		},
		"gpu-scratch": {
			ID: "gpu-scratch", Name: "GPU 机型：RAID1 系统 + 4 盘 RAID10 高速缓存",
			ApplicableModels: []string{"GX-8840"},
			MinDisks:         6, RequiredDiskType: "NVMe", RequiredModel: "NVME3T84-G5",
			Controller: "RAID-9560-16i", Personality: "RAID",
			Volumes: []domain.RAIDVolumeTemplate{
				{Name: "os", Level: "raid1", DiskCount: 2},
				{Name: "scratch", Level: "raid10", DiskCount: 4},
			},
			Partitions: []domain.PartitionSpec{
				{Mount: "/boot/efi", SizeMB: 1024, FileSystem: "vfat"},
				{Mount: "/", SizeMB: 153600, FileSystem: "xfs"},
				{Mount: "/scratch", SizeMB: 0, FileSystem: "xfs"},
			},
		},
	}
}
