package rds

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	rds_sdkv2 "github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/rds"
	"github.com/aws/smithy-go"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	"github.com/hashicorp/terraform-provider-aws/internal/create"
	"github.com/hashicorp/terraform-provider-aws/internal/errs"
	"github.com/hashicorp/terraform-provider-aws/internal/flex"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/tfresource"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
	"golang.org/x/exp/slices"
)

func ResourceInstance() *schema.Resource {
	return &schema.Resource{
		CreateWithoutTimeout: resourceInstanceCreate,
		ReadWithoutTimeout:   resourceInstanceRead,
		UpdateWithoutTimeout: resourceInstanceUpdate,
		DeleteWithoutTimeout: resourceInstanceDelete,

		Importer: &schema.ResourceImporter{
			StateContext: resourceInstanceImport,
		},

		SchemaVersion: 1,
		StateUpgraders: []schema.StateUpgrader{
			{
				Type:    resourceInstanceResourceV0().CoreConfigSchema().ImpliedType(),
				Upgrade: InstanceStateUpgradeV0,
				Version: 0,
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(40 * time.Minute),
			Update: schema.DefaultTimeout(80 * time.Minute),
			Delete: schema.DefaultTimeout(60 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			"address": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"allocated_storage": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					mas := d.Get("max_allocated_storage").(int)

					newInt, err := strconv.Atoi(new)

					if err != nil {
						return false
					}

					oldInt, err := strconv.Atoi(old)

					if err != nil {
						return false
					}

					// Allocated is higher than the configuration
					// and autoscaling is enabled
					if oldInt > newInt && mas > newInt {
						return true
					}

					return false
				},
			},
			"allow_major_version_upgrade": {
				Type:     schema.TypeBool,
				Optional: true,
			},
			// apply_immediately is used to determine when the update modifications
			// take place.
			// See http://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Overview.DBInstance.Modifying.html
			"apply_immediately": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
			},
			"arn": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"auto_minor_version_upgrade": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
			},
			"availability_zone": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"backup_retention_period": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},
			"backup_window": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: verify.ValidOnceADayWindowFormat,
			},
			"blue_green_update": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"enabled": {
							Type:     schema.TypeBool,
							Optional: true,
						},
					},
				},
			},
			"ca_cert_identifier": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"character_set_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"copy_tags_to_snapshot": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"custom_iam_instance_profile": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringMatch(regexp.MustCompile(`^AWSRDSCustom.*$`), "must begin with AWSRDSCustom"),
			},
			"customer_owned_ip_enabled": {
				Type:     schema.TypeBool,
				Optional: true,
			},
			"db_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
				ConflictsWith: []string{
					"name",
					"replicate_source_db",
				},
			},
			"db_subnet_group_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"delete_automated_backups": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
			},
			"deletion_protection": {
				Type:     schema.TypeBool,
				Optional: true,
			},
			"domain": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"domain_iam_role_name": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"enabled_cloudwatch_logs_exports": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Schema{
					Type:         schema.TypeString,
					ValidateFunc: validation.StringInSlice(InstanceExportableLogType_Values(), false),
				},
			},
			"endpoint": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"engine": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
				StateFunc: func(v interface{}) string {
					value := v.(string)
					return strings.ToLower(value)
				},
				ConflictsWith: []string{"replicate_source_db"},
			},
			"engine_version": {
				Type:          schema.TypeString,
				Optional:      true,
				Computed:      true,
				ConflictsWith: []string{"replicate_source_db"},
			},
			"engine_version_actual": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"final_snapshot_identifier": {
				Type:     schema.TypeString,
				Optional: true,
				ValidateFunc: validation.All(
					validation.StringMatch(regexp.MustCompile(`^[A-Za-z]`), "must begin with alphabetic character"),
					validation.StringMatch(regexp.MustCompile(`^[0-9A-Za-z-]+$`), "must only contain alphanumeric characters and hyphens"),
					validation.StringDoesNotMatch(regexp.MustCompile(`--`), "cannot contain two consecutive hyphens"),
					validation.StringDoesNotMatch(regexp.MustCompile(`-$`), "cannot end in a hyphen"),
				),
			},
			"hosted_zone_id": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"iam_database_authentication_enabled": {
				Type:     schema.TypeBool,
				Optional: true,
			},
			"identifier": {
				Type:          schema.TypeString,
				Optional:      true,
				Computed:      true,
				ForceNew:      true,
				ConflictsWith: []string{"identifier_prefix"},
				ValidateFunc:  validIdentifier,
			},
			"identifier_prefix": {
				Type:          schema.TypeString,
				Optional:      true,
				Computed:      true,
				ForceNew:      true,
				ConflictsWith: []string{"identifier"},
				ValidateFunc:  validIdentifierPrefix,
			},
			"instance_class": {
				Type:     schema.TypeString,
				Required: true,
			},
			"iops": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},
			"kms_key_id": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ForceNew:     true,
				ValidateFunc: verify.ValidARN,
			},
			"latest_restorable_time": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"license_model": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"maintenance_window": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				StateFunc: func(v interface{}) string {
					if v != nil {
						value := v.(string)
						return strings.ToLower(value)
					}
					return ""
				},
				ValidateFunc: verify.ValidOnceAWeekWindowFormat,
			},
			"max_allocated_storage": {
				Type:     schema.TypeInt,
				Optional: true,
				DiffSuppressFunc: func(k, old, new string, d *schema.ResourceData) bool {
					if old == "0" && new == fmt.Sprintf("%d", d.Get("allocated_storage").(int)) {
						return true
					}
					return false
				},
			},
			"monitoring_interval": {
				Type:     schema.TypeInt,
				Optional: true,
				Default:  0,
			},
			"monitoring_role_arn": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"multi_az": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
			},
			"name": {
				Type:       schema.TypeString,
				Optional:   true,
				Computed:   true,
				Deprecated: "Use db_name instead",
				ForceNew:   true,
				ConflictsWith: []string{
					"db_name",
					"replicate_source_db",
				},
			},
			"nchar_character_set_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"network_type": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringInSlice(NetworkType_Values(), false),
			},
			"option_group_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"parameter_group_name": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"password": {
				Type:      schema.TypeString,
				Optional:  true,
				Sensitive: true,
			},
			"performance_insights_enabled": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"performance_insights_kms_key_id": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: verify.ValidARN,
			},
			"performance_insights_retention_period": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},
			"port": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},
			"publicly_accessible": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"replica_mode": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringInSlice(rds.ReplicaMode_Values(), false),
			},
			"replicas": {
				Type:     schema.TypeList,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"replicate_source_db": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"resource_id": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"restore_to_point_in_time": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				ForceNew: true,
				ConflictsWith: []string{
					"s3_import",
					"snapshot_identifier",
					"replicate_source_db",
				},
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"restore_time": {
							Type:          schema.TypeString,
							Optional:      true,
							ValidateFunc:  verify.ValidUTCTimestamp,
							ConflictsWith: []string{"restore_to_point_in_time.0.use_latest_restorable_time"},
						},
						"source_db_instance_automated_backups_arn": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"source_db_instance_identifier": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"source_dbi_resource_id": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"use_latest_restorable_time": {
							Type:          schema.TypeBool,
							Optional:      true,
							ConflictsWith: []string{"restore_to_point_in_time.0.restore_time"},
						},
					},
				},
			},
			"s3_import": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				ConflictsWith: []string{
					"snapshot_identifier",
					"replicate_source_db",
				},
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"bucket_name": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},
						"bucket_prefix": {
							Type:     schema.TypeString,
							Optional: true,
							ForceNew: true,
						},
						"ingestion_role": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},
						"source_engine": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},
						"source_engine_version": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
						},
					},
				},
			},
			"security_group_names": {
				Type:       schema.TypeSet,
				Optional:   true,
				Elem:       &schema.Schema{Type: schema.TypeString},
				Deprecated: `With the retirement of EC2-Classic the security_group_names attribute has been deprecated and will be removed in a future version.`,
			},
			"skip_final_snapshot": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"snapshot_identifier": {
				Type:     schema.TypeString,
				Computed: true,
				Optional: true,
				ForceNew: true,
			},
			"status": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"storage_encrypted": {
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: true,
			},
			"storage_throughput": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},
			"storage_type": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringInSlice(StorageType_Values(), false),
			},
			"tags":     tftags.TagsSchema(),
			"tags_all": tftags.TagsSchemaComputed(),
			"timezone": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},
			"username": {
				Type:          schema.TypeString,
				Optional:      true,
				Computed:      true,
				ForceNew:      true,
				ConflictsWith: []string{"replicate_source_db"},
			},
			"vpc_security_group_ids": {
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
		},

		CustomizeDiff: customdiff.All(
			verify.SetTagsDiff,
			func(_ context.Context, d *schema.ResourceDiff, meta interface{}) error {
				if !d.Get("blue_green_update.0.enabled").(bool) {
					return nil
				}

				engine := d.Get("engine").(string)
				if !slices.Contains(dbInstanceValidBlueGreenEngines(), engine) {
					return fmt.Errorf(`"blue_green_update.enabled" cannot be set when "engine" is %q.`, engine)
				}
				return nil
			},
			func(_ context.Context, d *schema.ResourceDiff, meta interface{}) error {
				if !d.Get("blue_green_update.0.enabled").(bool) {
					return nil
				}

				source := d.Get("replicate_source_db").(string)
				if source != "" {
					return errors.New(`"blue_green_update.enabled" cannot be set when "replicate_source_db" is set.`)
				}
				return nil
			},
		),
	}
}

func resourceInstanceCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).RDSConn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	tags := defaultTagsConfig.MergeTags(tftags.New(d.Get("tags").(map[string]interface{})))

	if v, ok := d.GetOk("security_group_names"); ok && v.(*schema.Set).Len() > 0 {
		return errs.AppendErrorf(diags, `with the retirement of EC2-Classic no new RDS DB Instances can be created referencing RDS DB Security Groups`)
	}

	// Some API calls (e.g. CreateDBInstanceReadReplica and
	// RestoreDBInstanceFromDBSnapshot do not support all parameters to
	// correctly apply all settings in one pass. For missing parameters or
	// unsupported configurations, we may need to call ModifyDBInstance
	// afterwards to prevent Terraform operators from API errors or needing
	// to double apply.
	var requiresModifyDbInstance bool
	modifyDbInstanceInput := &rds.ModifyDBInstanceInput{
		ApplyImmediately: aws.Bool(true),
	}

	// Some ModifyDBInstance parameters (e.g. DBParameterGroupName) require
	// a database instance reboot to take effect. During resource creation,
	// we expect everything to be in sync before returning completion.
	var requiresRebootDbInstance bool

	identifier := create.Name(d.Get("identifier").(string), d.Get("identifier_prefix").(string))

	if v, ok := d.GetOk("replicate_source_db"); ok {
		sourceDBInstanceID := v.(string)
		input := &rds.CreateDBInstanceReadReplicaInput{
			AutoMinorVersionUpgrade:    aws.Bool(d.Get("auto_minor_version_upgrade").(bool)),
			CopyTagsToSnapshot:         aws.Bool(d.Get("copy_tags_to_snapshot").(bool)),
			DBInstanceClass:            aws.String(d.Get("instance_class").(string)),
			DBInstanceIdentifier:       aws.String(identifier),
			DeletionProtection:         aws.Bool(d.Get("deletion_protection").(bool)),
			PubliclyAccessible:         aws.Bool(d.Get("publicly_accessible").(bool)),
			SourceDBInstanceIdentifier: aws.String(sourceDBInstanceID),
			Tags:                       Tags(tags.IgnoreAWS()),
		}

		if _, ok := d.GetOk("allocated_storage"); ok {
			// RDS doesn't allow modifying the storage of a replica within the first 6h of creation.
			// allocated_storage is inherited from the primary so only the same value or no value is correct; a different value would fail the creation.
			// A different value is possible, granted: the value is higher than the current, there has been 6h between
			diags = errs.AppendWarningf(diags, `"allocated_storage" was ignored for DB Instance (%s) because a replica inherits the primary's allocated_storage and cannot be changed at creation.`, d.Id())
		}

		if v, ok := d.GetOk("availability_zone"); ok {
			input.AvailabilityZone = aws.String(v.(string))
		}

		if v, ok := d.GetOk("custom_iam_instance_profile"); ok {
			input.CustomIamInstanceProfile = aws.String(v.(string))
		}

		if v, ok := d.GetOk("db_subnet_group_name"); ok {
			input.DBSubnetGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("enabled_cloudwatch_logs_exports"); ok && v.(*schema.Set).Len() > 0 {
			input.EnableCloudwatchLogsExports = flex.ExpandStringSet(v.(*schema.Set))
		}

		if v, ok := d.GetOk("iam_database_authentication_enabled"); ok {
			input.EnableIAMDatabaseAuthentication = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("iops"); ok {
			input.Iops = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("kms_key_id"); ok {
			input.KmsKeyId = aws.String(v.(string))
			if arnParts := strings.Split(sourceDBInstanceID, ":"); len(arnParts) >= 4 {
				input.SourceRegion = aws.String(arnParts[3])
			}
		}

		if v, ok := d.GetOk("monitoring_interval"); ok {
			input.MonitoringInterval = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("monitoring_role_arn"); ok {
			input.MonitoringRoleArn = aws.String(v.(string))
		}

		if v, ok := d.GetOk("multi_az"); ok {
			input.MultiAZ = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("network_type"); ok {
			input.NetworkType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("option_group_name"); ok {
			input.OptionGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_enabled"); ok {
			input.EnablePerformanceInsights = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("performance_insights_kms_key_id"); ok {
			input.PerformanceInsightsKMSKeyId = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_retention_period"); ok {
			input.PerformanceInsightsRetentionPeriod = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("port"); ok {
			input.Port = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("replica_mode"); ok {
			input.ReplicaMode = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("storage_throughput"); ok {
			input.StorageThroughput = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("storage_type"); ok {
			input.StorageType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("vpc_security_group_ids"); ok && v.(*schema.Set).Len() > 0 {
			input.VpcSecurityGroupIds = flex.ExpandStringSet(v.(*schema.Set))
		}

		outputRaw, err := tfresource.RetryWhenAWSErrMessageContainsContext(ctx, propagationTimeout,
			func() (interface{}, error) {
				return conn.CreateDBInstanceReadReplicaWithContext(ctx, input)
			},
			errCodeInvalidParameterValue, "ENHANCED_MONITORING")

		if err != nil {
			return errs.AppendErrorf(diags, "creating RDS DB Instance (read replica) (%s): %s", identifier, err)
		}

		output := outputRaw.(*rds.CreateDBInstanceReadReplicaOutput)

		if v, ok := d.GetOk("allow_major_version_upgrade"); ok {
			// Having allowing_major_version_upgrade by itself should not trigger ModifyDBInstance
			// "InvalidParameterCombination: No modifications were requested".
			modifyDbInstanceInput.AllowMajorVersionUpgrade = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("backup_retention_period"); ok {
			if current, desired := aws.Int64Value(output.DBInstance.BackupRetentionPeriod), int64(v.(int)); current != desired {
				modifyDbInstanceInput.BackupRetentionPeriod = aws.Int64(desired)
				requiresModifyDbInstance = true
			}
		}

		if v, ok := d.GetOk("backup_window"); ok {
			if current, desired := aws.StringValue(output.DBInstance.PreferredBackupWindow), v.(string); current != desired {
				modifyDbInstanceInput.PreferredBackupWindow = aws.String(desired)
				requiresModifyDbInstance = true
			}
		}

		if v, ok := d.GetOk("ca_cert_identifier"); ok {
			if current, desired := aws.StringValue(output.DBInstance.CACertificateIdentifier), v.(string); current != desired {
				modifyDbInstanceInput.CACertificateIdentifier = aws.String(desired)
				requiresModifyDbInstance = true
			}
		}

		if v, ok := d.GetOk("maintenance_window"); ok {
			if current, desired := aws.StringValue(output.DBInstance.PreferredMaintenanceWindow), v.(string); current != desired {
				modifyDbInstanceInput.PreferredMaintenanceWindow = aws.String(desired)
				requiresModifyDbInstance = true
			}
		}

		if v, ok := d.GetOk("max_allocated_storage"); ok {
			if current, desired := aws.Int64Value(output.DBInstance.MaxAllocatedStorage), int64(v.(int)); current != desired {
				modifyDbInstanceInput.MaxAllocatedStorage = aws.Int64(desired)
				requiresModifyDbInstance = true
			}
		}

		if v, ok := d.GetOk("parameter_group_name"); ok {
			if len(output.DBInstance.DBParameterGroups) > 0 {
				if current, desired := aws.StringValue(output.DBInstance.DBParameterGroups[0].DBParameterGroupName), v.(string); current != desired {
					modifyDbInstanceInput.DBParameterGroupName = aws.String(desired)
					requiresModifyDbInstance = true
					requiresRebootDbInstance = true
				}
			}
		}

		if v, ok := d.GetOk("password"); ok {
			modifyDbInstanceInput.MasterUserPassword = aws.String(v.(string))
			requiresModifyDbInstance = true
		}
	} else if v, ok := d.GetOk("s3_import"); ok {
		dbName := d.Get("db_name").(string)
		if dbName == "" {
			dbName = d.Get("name").(string)
		}

		if _, ok := d.GetOk("allocated_storage"); !ok {
			diags = errs.AppendErrorf(diags, `"allocated_storage": required field is not set`)
		}
		if _, ok := d.GetOk("engine"); !ok {
			diags = errs.AppendErrorf(diags, `"engine": required field is not set`)
		}
		if _, ok := d.GetOk("password"); !ok {
			diags = errs.AppendErrorf(diags, `"password": required field is not set`)
		}
		if _, ok := d.GetOk("username"); !ok {
			diags = errs.AppendErrorf(diags, `"username": required field is not set`)
		}

		if _, ok := d.GetOk("character_set_name"); ok {
			diags = errs.AppendErrorf(diags, `"character_set_name" doesn't work with restores"`)
		}
		if _, ok := d.GetOk("timezone"); ok {
			diags = errs.AppendErrorf(diags, `"timezone" doesn't work with restores"`)
		}
		if diags.HasError() {
			return diags
		}

		tfMap := v.([]interface{})[0].(map[string]interface{})
		input := &rds.RestoreDBInstanceFromS3Input{
			AllocatedStorage:        aws.Int64(int64(d.Get("allocated_storage").(int))),
			AutoMinorVersionUpgrade: aws.Bool(d.Get("auto_minor_version_upgrade").(bool)),
			BackupRetentionPeriod:   aws.Int64(int64(d.Get("backup_retention_period").(int))),
			CopyTagsToSnapshot:      aws.Bool(d.Get("copy_tags_to_snapshot").(bool)),
			DBInstanceClass:         aws.String(d.Get("instance_class").(string)),
			DBInstanceIdentifier:    aws.String(identifier),
			DBName:                  aws.String(dbName),
			DeletionProtection:      aws.Bool(d.Get("deletion_protection").(bool)),
			Engine:                  aws.String(d.Get("engine").(string)),
			EngineVersion:           aws.String(d.Get("engine_version").(string)),
			MasterUsername:          aws.String(d.Get("username").(string)),
			MasterUserPassword:      aws.String(d.Get("password").(string)),
			PubliclyAccessible:      aws.Bool(d.Get("publicly_accessible").(bool)),
			S3BucketName:            aws.String(tfMap["bucket_name"].(string)),
			S3IngestionRoleArn:      aws.String(tfMap["ingestion_role"].(string)),
			S3Prefix:                aws.String(tfMap["bucket_prefix"].(string)),
			SourceEngine:            aws.String(tfMap["source_engine"].(string)),
			SourceEngineVersion:     aws.String(tfMap["source_engine_version"].(string)),
			StorageEncrypted:        aws.Bool(d.Get("storage_encrypted").(bool)),
			Tags:                    Tags(tags.IgnoreAWS()),
		}

		if v, ok := d.GetOk("availability_zone"); ok {
			input.AvailabilityZone = aws.String(v.(string))
		}

		if v, ok := d.GetOk("backup_window"); ok {
			input.PreferredBackupWindow = aws.String(v.(string))
		}

		if v, ok := d.GetOk("db_subnet_group_name"); ok {
			input.DBSubnetGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("iam_database_authentication_enabled"); ok {
			input.EnableIAMDatabaseAuthentication = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("iops"); ok {
			input.Iops = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("kms_key_id"); ok {
			input.KmsKeyId = aws.String(v.(string))
		}

		if v, ok := d.GetOk("license_model"); ok {
			input.LicenseModel = aws.String(v.(string))
		}

		if v, ok := d.GetOk("maintenance_window"); ok {
			input.PreferredMaintenanceWindow = aws.String(v.(string))
		}

		if v, ok := d.GetOk("monitoring_interval"); ok {
			input.MonitoringInterval = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("monitoring_role_arn"); ok {
			input.MonitoringRoleArn = aws.String(v.(string))
		}

		if v, ok := d.GetOk("multi_az"); ok {
			input.MultiAZ = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("network_type"); ok {
			input.NetworkType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("option_group_name"); ok {
			input.OptionGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("parameter_group_name"); ok {
			input.DBParameterGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_enabled"); ok {
			input.EnablePerformanceInsights = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("performance_insights_kms_key_id"); ok {
			input.PerformanceInsightsKMSKeyId = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_retention_period"); ok {
			input.PerformanceInsightsRetentionPeriod = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("port"); ok {
			input.Port = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("storage_throughput"); ok {
			input.StorageThroughput = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("storage_type"); ok {
			input.StorageType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("vpc_security_group_ids"); ok && v.(*schema.Set).Len() > 0 {
			input.VpcSecurityGroupIds = flex.ExpandStringSet(v.(*schema.Set))
		}

		_, err := tfresource.RetryWhenContext(ctx, propagationTimeout,
			func() (interface{}, error) {
				return conn.RestoreDBInstanceFromS3WithContext(ctx, input)
			},
			func(err error) (bool, error) {
				if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "ENHANCED_MONITORING") {
					return true, err
				}
				if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "S3_SNAPSHOT_INGESTION") {
					return true, err
				}
				if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "S3 bucket cannot be found") {
					return true, err
				}
				// InvalidParameterValue: Files from the specified Amazon S3 bucket cannot be downloaded. Make sure that you have created an AWS Identity and Access Management (IAM) role that lets Amazon RDS access Amazon S3 for you.
				if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "Files from the specified Amazon S3 bucket cannot be downloaded") {
					return true, err
				}

				return false, err
			},
		)

		if err != nil {
			return errs.AppendErrorf(diags, "creating RDS DB Instance (restore from S3) (%s): %s", identifier, err)
		}
	} else if v, ok := d.GetOk("snapshot_identifier"); ok {
		input := &rds.RestoreDBInstanceFromDBSnapshotInput{
			AutoMinorVersionUpgrade: aws.Bool(d.Get("auto_minor_version_upgrade").(bool)),
			CopyTagsToSnapshot:      aws.Bool(d.Get("copy_tags_to_snapshot").(bool)),
			DBInstanceClass:         aws.String(d.Get("instance_class").(string)),
			DBInstanceIdentifier:    aws.String(identifier),
			DBSnapshotIdentifier:    aws.String(v.(string)),
			DeletionProtection:      aws.Bool(d.Get("deletion_protection").(bool)),
			PubliclyAccessible:      aws.Bool(d.Get("publicly_accessible").(bool)),
			Tags:                    Tags(tags.IgnoreAWS()),
		}

		engine := strings.ToLower(d.Get("engine").(string))
		if v, ok := d.GetOk("db_name"); ok {
			// "Note: This parameter [DBName] doesn't apply to the MySQL, PostgreSQL, or MariaDB engines."
			// https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_RestoreDBInstanceFromDBSnapshot.html
			switch engine {
			case InstanceEngineMySQL, InstanceEnginePostgres, InstanceEngineMariaDB:
				// skip
			default:
				input.DBName = aws.String(v.(string))
			}
		} else if v, ok := d.GetOk("name"); ok {
			// "Note: This parameter [DBName] doesn't apply to the MySQL, PostgreSQL, or MariaDB engines."
			// https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_RestoreDBInstanceFromDBSnapshot.html
			switch engine {
			case InstanceEngineMySQL, InstanceEnginePostgres, InstanceEngineMariaDB:
				// skip
			default:
				input.DBName = aws.String(v.(string))
			}
		}

		if v, ok := d.GetOk("allocated_storage"); ok {
			modifyDbInstanceInput.AllocatedStorage = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("allow_major_version_upgrade"); ok {
			modifyDbInstanceInput.AllowMajorVersionUpgrade = aws.Bool(v.(bool))
			// Having allowing_major_version_upgrade by itself should not trigger ModifyDBInstance
			// InvalidParameterCombination: No modifications were requested
		}

		if v, ok := d.GetOk("availability_zone"); ok {
			input.AvailabilityZone = aws.String(v.(string))
		}

		if v, ok := d.GetOkExists("backup_retention_period"); ok {
			modifyDbInstanceInput.BackupRetentionPeriod = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("backup_window"); ok {
			modifyDbInstanceInput.PreferredBackupWindow = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("custom_iam_instance_profile"); ok {
			input.CustomIamInstanceProfile = aws.String(v.(string))
		}

		if v, ok := d.GetOk("customer_owned_ip_enabled"); ok {
			input.EnableCustomerOwnedIp = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("db_subnet_group_name"); ok {
			input.DBSubnetGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("domain"); ok {
			input.Domain = aws.String(v.(string))
		}

		if v, ok := d.GetOk("domain_iam_role_name"); ok {
			input.DomainIAMRoleName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("enabled_cloudwatch_logs_exports"); ok && v.(*schema.Set).Len() > 0 {
			input.EnableCloudwatchLogsExports = flex.ExpandStringSet(v.(*schema.Set))
		}

		if engine != "" {
			input.Engine = aws.String(engine)
		}

		if v, ok := d.GetOk("engine_version"); ok {
			modifyDbInstanceInput.EngineVersion = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("iam_database_authentication_enabled"); ok {
			input.EnableIAMDatabaseAuthentication = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("iops"); ok {
			modifyDbInstanceInput.Iops = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("license_model"); ok {
			input.LicenseModel = aws.String(v.(string))
		}

		if v, ok := d.GetOk("maintenance_window"); ok {
			modifyDbInstanceInput.PreferredMaintenanceWindow = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("max_allocated_storage"); ok {
			modifyDbInstanceInput.MaxAllocatedStorage = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("monitoring_interval"); ok {
			modifyDbInstanceInput.MonitoringInterval = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("monitoring_role_arn"); ok {
			modifyDbInstanceInput.MonitoringRoleArn = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("multi_az"); ok {
			// When using SQL Server engine with MultiAZ enabled, its not
			// possible to immediately enable mirroring since
			// BackupRetentionPeriod is not available as a parameter to
			// RestoreDBInstanceFromDBSnapshot and you receive an error. e.g.
			// InvalidParameterValue: Mirroring cannot be applied to instances with backup retention set to zero.
			// If we know the engine, prevent the error upfront.
			if strings.HasPrefix(engine, "sqlserver") {
				modifyDbInstanceInput.MultiAZ = aws.Bool(v.(bool))
				requiresModifyDbInstance = true
			} else {
				input.MultiAZ = aws.Bool(v.(bool))
			}
		}

		if v, ok := d.GetOk("network_type"); ok {
			input.NetworkType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("option_group_name"); ok {
			input.OptionGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("parameter_group_name"); ok {
			input.DBParameterGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("password"); ok {
			modifyDbInstanceInput.MasterUserPassword = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("performance_insights_enabled"); ok {
			modifyDbInstanceInput.EnablePerformanceInsights = aws.Bool(v.(bool))
			requiresModifyDbInstance = true

			if v, ok := d.GetOk("performance_insights_kms_key_id"); ok {
				modifyDbInstanceInput.PerformanceInsightsKMSKeyId = aws.String(v.(string))
			}

			if v, ok := d.GetOk("performance_insights_retention_period"); ok {
				modifyDbInstanceInput.PerformanceInsightsRetentionPeriod = aws.Int64(int64(v.(int)))
			}
		}

		if v, ok := d.GetOk("port"); ok {
			input.Port = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("storage_throughput"); ok {
			modifyDbInstanceInput.StorageThroughput = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("storage_type"); ok {
			modifyDbInstanceInput.StorageType = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("tde_credential_arn"); ok {
			input.TdeCredentialArn = aws.String(v.(string))
		}

		if v := d.Get("vpc_security_group_ids").(*schema.Set); v.Len() > 0 {
			input.VpcSecurityGroupIds = flex.ExpandStringSet(v)
		}

		_, err := tfresource.RetryWhenContext(ctx, propagationTimeout,
			func() (interface{}, error) {
				return conn.RestoreDBInstanceFromDBSnapshotWithContext(ctx, input)
			},
			func(err error) (bool, error) {
				if tfawserr.ErrMessageContains(err, errCodeValidationError, "RDS couldn't fetch the role from instance profile") {
					return true, err
				}

				return false, err
			},
		)

		// When using SQL Server engine with MultiAZ enabled, its not
		// possible to immediately enable mirroring since
		// BackupRetentionPeriod is not available as a parameter to
		// RestoreDBInstanceFromDBSnapshot and you receive an error. e.g.
		// InvalidParameterValue: Mirroring cannot be applied to instances with backup retention set to zero.
		// Since engine is not a required argument when using snapshot_identifier
		// and the RDS API determines this condition, we catch the error
		// and remove the invalid configuration for it to be fixed afterwards.
		if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "Mirroring cannot be applied to instances with backup retention set to zero") {
			input.MultiAZ = aws.Bool(false)
			modifyDbInstanceInput.MultiAZ = aws.Bool(true)
			requiresModifyDbInstance = true
			_, err = conn.RestoreDBInstanceFromDBSnapshotWithContext(ctx, input)
		}

		if err != nil {
			return errs.AppendErrorf(diags, "creating RDS DB Instance (restore from snapshot) (%s): %s", identifier, err)
		}
	} else if v, ok := d.GetOk("restore_to_point_in_time"); ok {
		tfMap := v.([]interface{})[0].(map[string]interface{})

		input := &rds.RestoreDBInstanceToPointInTimeInput{
			AutoMinorVersionUpgrade:    aws.Bool(d.Get("auto_minor_version_upgrade").(bool)),
			CopyTagsToSnapshot:         aws.Bool(d.Get("copy_tags_to_snapshot").(bool)),
			DBInstanceClass:            aws.String(d.Get("instance_class").(string)),
			DeletionProtection:         aws.Bool(d.Get("deletion_protection").(bool)),
			PubliclyAccessible:         aws.Bool(d.Get("publicly_accessible").(bool)),
			Tags:                       Tags(tags.IgnoreAWS()),
			TargetDBInstanceIdentifier: aws.String(identifier),
		}

		if v, ok := tfMap["restore_time"].(string); ok && v != "" {
			v, _ := time.Parse(time.RFC3339, v)

			input.RestoreTime = aws.Time(v)
		}

		if v, ok := tfMap["source_db_instance_automated_backups_arn"].(string); ok && v != "" {
			input.SourceDBInstanceAutomatedBackupsArn = aws.String(v)
		}

		if v, ok := tfMap["source_db_instance_identifier"].(string); ok && v != "" {
			input.SourceDBInstanceIdentifier = aws.String(v)
		}

		if v, ok := tfMap["source_dbi_resource_id"].(string); ok && v != "" {
			input.SourceDbiResourceId = aws.String(v)
		}

		if v, ok := tfMap["use_latest_restorable_time"].(bool); ok && v {
			input.UseLatestRestorableTime = aws.Bool(v)
		}

		if v, ok := d.GetOk("availability_zone"); ok {
			input.AvailabilityZone = aws.String(v.(string))
		}

		if v, ok := d.GetOk("custom_iam_instance_profile"); ok {
			input.CustomIamInstanceProfile = aws.String(v.(string))
		}

		if v, ok := d.GetOk("customer_owned_ip_enabled"); ok {
			input.EnableCustomerOwnedIp = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("db_name"); ok {
			input.DBName = aws.String(v.(string))
		} else if v, ok := d.GetOk("name"); ok {
			input.DBName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("db_subnet_group_name"); ok {
			input.DBSubnetGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("domain"); ok {
			input.Domain = aws.String(v.(string))
		}

		if v, ok := d.GetOk("domain_iam_role_name"); ok {
			input.DomainIAMRoleName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("enabled_cloudwatch_logs_exports"); ok && v.(*schema.Set).Len() > 0 {
			input.EnableCloudwatchLogsExports = flex.ExpandStringSet(v.(*schema.Set))
		}

		if v, ok := d.GetOk("engine"); ok {
			input.Engine = aws.String(v.(string))
		}

		if v, ok := d.GetOk("iam_database_authentication_enabled"); ok {
			input.EnableIAMDatabaseAuthentication = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("iops"); ok {
			input.Iops = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("license_model"); ok {
			input.LicenseModel = aws.String(v.(string))
		}

		if v, ok := d.GetOk("max_allocated_storage"); ok {
			input.MaxAllocatedStorage = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("monitoring_interval"); ok {
			modifyDbInstanceInput.MonitoringInterval = aws.Int64(int64(v.(int)))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("monitoring_role_arn"); ok {
			modifyDbInstanceInput.MonitoringRoleArn = aws.String(v.(string))
			requiresModifyDbInstance = true
		}

		if v, ok := d.GetOk("multi_az"); ok {
			input.MultiAZ = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("option_group_name"); ok {
			input.OptionGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("parameter_group_name"); ok {
			input.DBParameterGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("port"); ok {
			input.Port = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("storage_type"); ok {
			input.StorageType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("storage_type"); ok {
			input.StorageType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("tde_credential_arn"); ok {
			input.TdeCredentialArn = aws.String(v.(string))
		}

		if v, ok := d.GetOk("vpc_security_group_ids"); ok && v.(*schema.Set).Len() > 0 {
			input.VpcSecurityGroupIds = flex.ExpandStringSet(v.(*schema.Set))
		}

		_, err := tfresource.RetryWhenContext(ctx, propagationTimeout,
			func() (interface{}, error) {
				return conn.RestoreDBInstanceToPointInTimeWithContext(ctx, input)
			},
			func(err error) (bool, error) {
				if tfawserr.ErrMessageContains(err, errCodeValidationError, "RDS couldn't fetch the role from instance profile") {
					return true, err
				}

				return false, err
			},
		)

		if err != nil {
			return errs.AppendErrorf(diags, "creating RDS DB Instance (restore to point-in-time) (%s): %s", identifier, err)
		}
	} else {
		dbName := d.Get("db_name").(string)
		if dbName == "" {
			dbName = d.Get("name").(string)
		}

		if _, ok := d.GetOk("allocated_storage"); !ok {
			diags = errs.AppendErrorf(diags, `"allocated_storage": required field is not set`)
		}
		if _, ok := d.GetOk("engine"); !ok {
			diags = errs.AppendErrorf(diags, `"engine": required field is not set`)
		}
		if _, ok := d.GetOk("password"); !ok {
			diags = errs.AppendErrorf(diags, `"password": required field is not set`)
		}
		if _, ok := d.GetOk("username"); !ok {
			diags = errs.AppendErrorf(diags, `"username": required field is not set`)
		}
		if diags.HasError() {
			return diags
		}

		input := &rds.CreateDBInstanceInput{
			AllocatedStorage:        aws.Int64(int64(d.Get("allocated_storage").(int))),
			AutoMinorVersionUpgrade: aws.Bool(d.Get("auto_minor_version_upgrade").(bool)),
			BackupRetentionPeriod:   aws.Int64(int64(d.Get("backup_retention_period").(int))),
			CopyTagsToSnapshot:      aws.Bool(d.Get("copy_tags_to_snapshot").(bool)),
			DBInstanceClass:         aws.String(d.Get("instance_class").(string)),
			DBInstanceIdentifier:    aws.String(identifier),
			DBName:                  aws.String(dbName),
			DeletionProtection:      aws.Bool(d.Get("deletion_protection").(bool)),
			Engine:                  aws.String(d.Get("engine").(string)),
			EngineVersion:           aws.String(d.Get("engine_version").(string)),
			MasterUsername:          aws.String(d.Get("username").(string)),
			MasterUserPassword:      aws.String(d.Get("password").(string)),
			PubliclyAccessible:      aws.Bool(d.Get("publicly_accessible").(bool)),
			StorageEncrypted:        aws.Bool(d.Get("storage_encrypted").(bool)),
			Tags:                    Tags(tags.IgnoreAWS()),
		}

		if v, ok := d.GetOk("availability_zone"); ok {
			input.AvailabilityZone = aws.String(v.(string))
		}

		if v, ok := d.GetOk("backup_window"); ok {
			input.PreferredBackupWindow = aws.String(v.(string))
		}

		if v, ok := d.GetOk("character_set_name"); ok {
			input.CharacterSetName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("custom_iam_instance_profile"); ok {
			input.CustomIamInstanceProfile = aws.String(v.(string))
		}

		if v, ok := d.GetOk("customer_owned_ip_enabled"); ok {
			input.EnableCustomerOwnedIp = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("db_subnet_group_name"); ok {
			input.DBSubnetGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("domain"); ok {
			input.Domain = aws.String(v.(string))
		}

		if v, ok := d.GetOk("domain_iam_role_name"); ok {
			input.DomainIAMRoleName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("enabled_cloudwatch_logs_exports"); ok && v.(*schema.Set).Len() > 0 {
			input.EnableCloudwatchLogsExports = flex.ExpandStringSet(v.(*schema.Set))
		}

		if v, ok := d.GetOk("iam_database_authentication_enabled"); ok {
			input.EnableIAMDatabaseAuthentication = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("iops"); ok {
			input.Iops = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("kms_key_id"); ok {
			input.KmsKeyId = aws.String(v.(string))
		}

		if v, ok := d.GetOk("license_model"); ok {
			input.LicenseModel = aws.String(v.(string))
		}

		if v, ok := d.GetOk("maintenance_window"); ok {
			input.PreferredMaintenanceWindow = aws.String(v.(string))
		}

		if v, ok := d.GetOk("max_allocated_storage"); ok {
			input.MaxAllocatedStorage = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("monitoring_interval"); ok {
			input.MonitoringInterval = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("monitoring_role_arn"); ok {
			input.MonitoringRoleArn = aws.String(v.(string))
		}

		if v, ok := d.GetOk("multi_az"); ok {
			input.MultiAZ = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("nchar_character_set_name"); ok {
			input.NcharCharacterSetName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("network_type"); ok {
			input.NetworkType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("option_group_name"); ok {
			input.OptionGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("parameter_group_name"); ok {
			input.DBParameterGroupName = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_enabled"); ok {
			input.EnablePerformanceInsights = aws.Bool(v.(bool))
		}

		if v, ok := d.GetOk("performance_insights_kms_key_id"); ok {
			input.PerformanceInsightsKMSKeyId = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_retention_period"); ok {
			input.PerformanceInsightsRetentionPeriod = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("port"); ok {
			input.Port = aws.Int64(int64(v.(int)))
		}

		if v := d.Get("security_group_names").(*schema.Set); v.Len() > 0 {
			input.DBSecurityGroups = flex.ExpandStringSet(v)
		}

		if v, ok := d.GetOk("storage_throughput"); ok {
			input.StorageThroughput = aws.Int64(int64(v.(int)))
		}

		if v, ok := d.GetOk("storage_type"); ok {
			input.StorageType = aws.String(v.(string))
		}

		if v, ok := d.GetOk("timezone"); ok {
			input.Timezone = aws.String(v.(string))
		}

		if v := d.Get("vpc_security_group_ids").(*schema.Set); v.Len() > 0 {
			input.VpcSecurityGroupIds = flex.ExpandStringSet(v)
		}

		outputRaw, err := tfresource.RetryWhenContext(ctx, propagationTimeout,
			func() (interface{}, error) {
				return conn.CreateDBInstanceWithContext(ctx, input)
			},
			func(err error) (bool, error) {
				if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "ENHANCED_MONITORING") {
					return true, err
				}
				if tfawserr.ErrMessageContains(err, errCodeValidationError, "RDS couldn't fetch the role from instance profile") {
					return true, err
				}

				return false, err
			},
		)

		if err != nil {
			return errs.AppendErrorf(diags, "creating RDS DB Instance (%s): %s", identifier, err)
		}

		output := outputRaw.(*rds.CreateDBInstanceOutput)

		// This is added here to avoid unnecessary modification when ca_cert_identifier is the default one
		if v, ok := d.GetOk("ca_cert_identifier"); ok && v.(string) != aws.StringValue(output.DBInstance.CACertificateIdentifier) {
			modifyDbInstanceInput.CACertificateIdentifier = aws.String(v.(string))
			requiresModifyDbInstance = true
		}
	}

	d.SetId(identifier)

	if _, err := waitDBInstanceAvailableSDKv1(ctx, conn, d.Id(), d.Timeout(schema.TimeoutCreate)); err != nil {
		return errs.AppendErrorf(diags, "waiting for RDS DB Instance (%s) create: %s", d.Id(), err)
	}

	if requiresModifyDbInstance {
		modifyDbInstanceInput.DBInstanceIdentifier = aws.String(d.Id())

		_, err := conn.ModifyDBInstanceWithContext(ctx, modifyDbInstanceInput)

		if err != nil {
			return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
		}

		if _, err := waitDBInstanceAvailableSDKv1(ctx, conn, d.Id(), d.Timeout(schema.TimeoutUpdate)); err != nil {
			return errs.AppendErrorf(diags, "waiting for RDS DB Instance (%s) update: %s", d.Id(), err)
		}
	}

	if requiresRebootDbInstance {
		_, err := conn.RebootDBInstanceWithContext(ctx, &rds.RebootDBInstanceInput{
			DBInstanceIdentifier: aws.String(d.Id()),
		})

		if err != nil {
			return errs.AppendErrorf(diags, "rebooting RDS DB Instance (%s): %s", d.Id(), err)
		}

		if _, err := waitDBInstanceAvailableSDKv1(ctx, conn, d.Id(), d.Timeout(schema.TimeoutUpdate)); err != nil {
			return errs.AppendErrorf(diags, "waiting for RDS DB Instance (%s) update: %s", d.Id(), err)
		}
	}

	return append(diags, resourceInstanceRead(ctx, d, meta)...)
}

func resourceInstanceRead(ctx context.Context, d *schema.ResourceData, meta interface{}) (diags diag.Diagnostics) {
	conn := meta.(*conns.AWSClient).RDSConn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	ignoreTagsConfig := meta.(*conns.AWSClient).IgnoreTagsConfig

	v, err := findDBInstanceByIDSDKv1(ctx, conn, d.Id())

	if !d.IsNewResource() && tfresource.NotFound(err) {
		log.Printf("[WARN] RDS DB Instance (%s) not found, removing from state", d.Id())
		d.SetId("")
		return nil
	}

	if err != nil {
		return errs.AppendErrorf(diags, "reading RDS DB Instance (%s): %s", d.Id(), err)
	}

	d.Set("allocated_storage", v.AllocatedStorage)
	arn := aws.StringValue(v.DBInstanceArn)
	d.Set("arn", arn)
	d.Set("auto_minor_version_upgrade", v.AutoMinorVersionUpgrade)
	d.Set("availability_zone", v.AvailabilityZone)
	d.Set("backup_retention_period", v.BackupRetentionPeriod)
	d.Set("backup_window", v.PreferredBackupWindow)
	d.Set("ca_cert_identifier", v.CACertificateIdentifier)
	d.Set("character_set_name", v.CharacterSetName)
	d.Set("copy_tags_to_snapshot", v.CopyTagsToSnapshot)
	d.Set("custom_iam_instance_profile", v.CustomIamInstanceProfile)
	d.Set("customer_owned_ip_enabled", v.CustomerOwnedIpEnabled)
	d.Set("db_name", v.DBName)
	if v.DBSubnetGroup != nil {
		d.Set("db_subnet_group_name", v.DBSubnetGroup.DBSubnetGroupName)
	}
	d.Set("deletion_protection", v.DeletionProtection)
	if len(v.DomainMemberships) > 0 && v.DomainMemberships[0] != nil {
		d.Set("domain", v.DomainMemberships[0].Domain)
		d.Set("domain_iam_role_name", v.DomainMemberships[0].IAMRoleName)
	} else {
		d.Set("domain", nil)
		d.Set("domain_iam_role_name", nil)
	}
	d.Set("enabled_cloudwatch_logs_exports", aws.StringValueSlice(v.EnabledCloudwatchLogsExports))
	d.Set("engine", v.Engine)
	d.Set("iam_database_authentication_enabled", v.IAMDatabaseAuthenticationEnabled)
	d.Set("identifier", v.DBInstanceIdentifier)
	d.Set("identifier_prefix", create.NamePrefixFromName(aws.StringValue(v.DBInstanceIdentifier)))
	d.Set("instance_class", v.DBInstanceClass)
	d.Set("iops", v.Iops)
	d.Set("kms_key_id", v.KmsKeyId)
	if v.LatestRestorableTime != nil {
		d.Set("latest_restorable_time", aws.TimeValue(v.LatestRestorableTime).Format(time.RFC3339))
	} else {
		d.Set("latest_restorable_time", nil)
	}
	d.Set("license_model", v.LicenseModel)
	d.Set("maintenance_window", v.PreferredMaintenanceWindow)
	d.Set("max_allocated_storage", v.MaxAllocatedStorage)
	d.Set("monitoring_interval", v.MonitoringInterval)
	d.Set("monitoring_role_arn", v.MonitoringRoleArn)
	d.Set("multi_az", v.MultiAZ)
	d.Set("name", v.DBName)
	d.Set("nchar_character_set_name", v.NcharCharacterSetName)
	d.Set("network_type", v.NetworkType)
	if len(v.OptionGroupMemberships) > 0 && v.OptionGroupMemberships[0] != nil {
		d.Set("option_group_name", v.OptionGroupMemberships[0].OptionGroupName)
	}
	if len(v.DBParameterGroups) > 0 && v.DBParameterGroups[0] != nil {
		d.Set("parameter_group_name", v.DBParameterGroups[0].DBParameterGroupName)
	}
	d.Set("performance_insights_enabled", v.PerformanceInsightsEnabled)
	d.Set("performance_insights_kms_key_id", v.PerformanceInsightsKMSKeyId)
	d.Set("performance_insights_retention_period", v.PerformanceInsightsRetentionPeriod)
	d.Set("port", v.DbInstancePort)
	d.Set("publicly_accessible", v.PubliclyAccessible)
	d.Set("replica_mode", v.ReplicaMode)
	d.Set("replicas", aws.StringValueSlice(v.ReadReplicaDBInstanceIdentifiers))
	d.Set("replicate_source_db", v.ReadReplicaSourceDBInstanceIdentifier)
	d.Set("resource_id", v.DbiResourceId)
	var securityGroupNames []string
	for _, v := range v.DBSecurityGroups {
		securityGroupNames = append(securityGroupNames, aws.StringValue(v.DBSecurityGroupName))
	}
	d.Set("security_group_names", securityGroupNames)
	d.Set("status", v.DBInstanceStatus)
	d.Set("storage_encrypted", v.StorageEncrypted)
	d.Set("storage_throughput", v.StorageThroughput)
	d.Set("storage_type", v.StorageType)
	d.Set("timezone", v.Timezone)
	d.Set("username", v.MasterUsername)
	var vpcSecurityGroupIDs []string
	for _, v := range v.VpcSecurityGroups {
		vpcSecurityGroupIDs = append(vpcSecurityGroupIDs, aws.StringValue(v.VpcSecurityGroupId))
	}
	d.Set("vpc_security_group_ids", vpcSecurityGroupIDs)

	if v.Endpoint != nil {
		d.Set("address", v.Endpoint.Address)
		if v.Endpoint.Address != nil && v.Endpoint.Port != nil {
			d.Set("endpoint", fmt.Sprintf("%s:%d", aws.StringValue(v.Endpoint.Address), aws.Int64Value(v.Endpoint.Port)))
		}
		d.Set("hosted_zone_id", v.Endpoint.HostedZoneId)
		d.Set("port", v.Endpoint.Port)
	}

	dbSetResourceDataEngineVersionFromInstance(d, v)

	tags, err := ListTagsWithContext(ctx, conn, arn)

	if err != nil {
		return errs.AppendErrorf(diags, "listing tags for RDS DB Instance (%s): %s", arn, err)
	}

	tags = tags.IgnoreAWS().IgnoreConfig(ignoreTagsConfig)

	//lintignore:AWSR002
	if err := d.Set("tags", tags.RemoveDefaultConfig(defaultTagsConfig).Map()); err != nil {
		return errs.AppendErrorf(diags, "setting tags: %s", err)
	}

	if err := d.Set("tags_all", tags.Map()); err != nil {
		return errs.AppendErrorf(diags, "setting tags_all: %s", err)
	}

	return diags
}

func resourceInstanceUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) (diags diag.Diagnostics) {
	conn := meta.(*conns.AWSClient).RDSClient()
	deadline := NewDeadline(d.Timeout(schema.TimeoutUpdate))

	// Separate request to promote a database.
	if d.HasChange("replicate_source_db") {
		if d.Get("replicate_source_db").(string) == "" {
			input := &rds_sdkv2.PromoteReadReplicaInput{
				BackupRetentionPeriod: aws.Int32(int32(d.Get("backup_retention_period").(int))),
				DBInstanceIdentifier:  aws.String(d.Id()),
			}

			if attr, ok := d.GetOk("backup_window"); ok {
				input.PreferredBackupWindow = aws.String(attr.(string))
			}

			_, err := conn.PromoteReadReplica(ctx, input)
			if err != nil {
				return errs.AppendErrorf(diags, "promoting RDS DB Instance (%s): %s", d.Id(), err)
			}

			if _, err := waitDBInstanceAvailableSDKv2(ctx, conn, d.Id(), deadline.remaining()); err != nil {
				return errs.AppendErrorf(diags, "promoting RDS DB Instance (%s): waiting for completion: %s", d.Id(), err)
			}
		} else {
			return errs.AppendErrorf(diags, "cannot elect new source database for replication")
		}
	}

	// Having allowing_major_version_upgrade by itself should not trigger ModifyDBInstance
	// as it results in "InvalidParameterCombination: No modifications were requested".
	if d.HasChangesExcept(
		"allow_major_version_upgrade",
		"blue_green_update",
		"delete_automated_backups",
		"final_snapshot_identifier",
		"replicate_source_db",
		"skip_final_snapshot",
		"tags", "tags_all",
	) {
		if d.Get("blue_green_update.0.enabled").(bool) {
			orchestrator := newBlueGreenOrchestrator(conn)
			handler := newInstanceHandler(conn)
			var cleaupWaiters []func(optFns ...tfresource.OptionsFunc)
			defer func() {
				if len(cleaupWaiters) == 0 {
					return
				}

				waiter, waiters := cleaupWaiters[0], cleaupWaiters[1:]
				waiter()
				for _, waiter := range waiters {
					// Skip the delay for subsequent waiters. Since we're waiting for all of the waiters
					// to complete, we don't need to run them concurrently, saving on network traffic.
					waiter(tfresource.WithDelay(0))
				}
			}()

			err := handler.precondition(ctx, d)
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}

			createIn := handler.createBlueGreenInput(d)

			log.Printf("[DEBUG] Updating RDS DB Instance (%s): Creating Blue/Green Deployment", d.Id())

			dep, err := orchestrator.createDeployment(ctx, createIn)
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}
			deploymentIdentifier := dep.BlueGreenDeploymentIdentifier
			defer func() {
				log.Printf("[DEBUG] Updating RDS DB Instance (%s): Deleting Blue/Green Deployment", d.Id())

				if dep == nil {
					log.Printf("[DEBUG] Updating RDS DB Instance (%s): Deleting Blue/Green Deployment: deployment disappeared", d.Id())
					return
				}

				// Ensure that the Blue/Green Deployment is always cleaned up
				input := &rds_sdkv2.DeleteBlueGreenDeploymentInput{
					BlueGreenDeploymentIdentifier: deploymentIdentifier,
				}
				if aws.StringValue(dep.Status) != "SWITCHOVER_COMPLETED" {
					input.DeleteTarget = aws.Bool(true)
				}
				_, err = conn.DeleteBlueGreenDeployment(ctx, input)
				if err != nil {
					diags = errs.AppendErrorf(diags, "updating RDS DB Instance (%s): deleting Blue/Green Deployment: %s", d.Id(), err)
					return
				}

				cleaupWaiters = append(cleaupWaiters, func(optFns ...tfresource.OptionsFunc) {
					_, err = waitBlueGreenDeploymentDeleted(ctx, conn, aws.StringValue(deploymentIdentifier), deadline.remaining(), optFns...)
					if err != nil {
						diags = errs.AppendErrorf(diags, "updating RDS DB Instance (%s): deleting Blue/Green Deployment: waiting for completion: %s", d.Id(), err)
					}
				})
			}()

			dep, err = orchestrator.waitForDeploymentAvailable(ctx, aws.StringValue(dep.BlueGreenDeploymentIdentifier), deadline.remaining())
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}

			targetARN, err := parseDBInstanceARN(aws.StringValue(dep.Target))
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): creating Blue/Green Deployment: waiting for Green environment: %s", d.Id(), err)
			}
			_, err = waitDBInstanceAvailableSDKv2(ctx, conn, targetARN.Identifier, deadline.remaining())
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): creating Blue/Green Deployment: waiting for Green environment: %s", d.Id(), err)
			}

			err = handler.modifyTarget(ctx, targetARN.Identifier, d, deadline.remaining(), fmt.Sprintf("Updating RDS DB Instance (%s)", d.Id()))
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}

			log.Printf("[DEBUG] Updating RDS DB Instance (%s): Switching over Blue/Green Deployment", d.Id())

			dep, err = orchestrator.switchover(ctx, aws.StringValue(dep.BlueGreenDeploymentIdentifier), deadline.remaining())
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}

			log.Printf("[DEBUG] Updating RDS DB Instance (%s): Deleting Blue/Green Deployment source", d.Id())

			sourceARN, err := parseDBInstanceARN(aws.StringValue(dep.Source))
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): deleting Blue/Green Deployment source: %s", d.Id(), err)
			}
			if d.Get("deletion_protection").(bool) {
				input := &rds_sdkv2.ModifyDBInstanceInput{
					ApplyImmediately:     true,
					DBInstanceIdentifier: aws.String(sourceARN.Identifier),
					DeletionProtection:   aws.Bool(false),
				}
				err := dbInstanceModify(ctx, conn, input, deadline.remaining())
				if err != nil {
					return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): deleting Blue/Green Deployment source: disabling deletion protection: %s", d.Id(), err)
				}
			}
			deleteInput := &rds_sdkv2.DeleteDBInstanceInput{
				DBInstanceIdentifier: aws.String(sourceARN.Identifier),
				SkipFinalSnapshot:    true,
			}
			_, err = tfresource.RetryWhenContext(ctx, 5*time.Minute,
				func() (any, error) {
					return conn.DeleteDBInstance(ctx, deleteInput)
				},
				func(err error) (bool, error) {
					// Retry for IAM eventual consistency.
					apiErr, ok := errs.As[smithy.APIError](err)
					if ok && apiErr.ErrorCode() == errCodeInvalidParameterValue && strings.Contains(apiErr.ErrorMessage(), "IAM role ARN value is invalid or does not include the required permissions") {
						return true, err
					}

					if ok && apiErr.ErrorCode() == errCodeInvalidParameterCombination && strings.Contains(apiErr.ErrorMessage(), "disable deletion pro") {
						return true, err
					}

					return false, err
				},
			)
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): deleting Blue/Green Deployment source: %s", d.Id(), err)
			}

			cleaupWaiters = append(cleaupWaiters, func(optFns ...tfresource.OptionsFunc) {
				_, err = waitDBInstanceDeleted(ctx, meta.(*conns.AWSClient).RDSConn, sourceARN.Identifier, deadline.remaining(), optFns...)
				if err != nil {
					diags = errs.AppendErrorf(diags, "updating RDS DB Instance (%s): deleting Blue/Green Deployment source: waiting for completion: %s", d.Id(), err)
				}
			})

			if diags.HasError() {
				return
			}
		} else {
			input := &rds_sdkv2.ModifyDBInstanceInput{
				ApplyImmediately:     d.Get("apply_immediately").(bool),
				DBInstanceIdentifier: aws.String(d.Id()),
			}

			if !input.ApplyImmediately {
				log.Println("[INFO] Only settings updating, instance changes will be applied in next maintenance window")
			}

			dbInstancePopulateModify(input, d)

			if d.HasChange("engine_version") {
				input.EngineVersion = aws.String(d.Get("engine_version").(string))
				input.AllowMajorVersionUpgrade = d.Get("allow_major_version_upgrade").(bool)
			}

			if d.HasChange("parameter_group_name") {
				input.DBParameterGroupName = aws.String(d.Get("parameter_group_name").(string))
			}

			err := dbInstanceModify(ctx, conn, input, deadline.remaining())
			if err != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}
		}
	}

	if d.HasChange("tags_all") {
		o, n := d.GetChange("tags_all")

		if err := UpdateTagsWithContext(ctx, meta.(*conns.AWSClient).RDSConn, d.Get("arn").(string), o, n); err != nil {
			return errs.AppendErrorf(diags, "updating RDS DB Instance (%s) tags: %s", d.Get("arn").(string), err)
		}
	}

	return append(diags, resourceInstanceRead(ctx, d, meta)...)
}

func dbInstancePopulateModify(input *rds_sdkv2.ModifyDBInstanceInput, d *schema.ResourceData) bool {
	needsModify := false

	if d.HasChanges("allocated_storage", "iops") {
		needsModify = true
		input.Iops = aws.Int32(int32(d.Get("iops").(int)))
		input.AllocatedStorage = aws.Int32(int32(d.Get("allocated_storage").(int)))
	}

	if d.HasChange("auto_minor_version_upgrade") {
		needsModify = true
		input.AutoMinorVersionUpgrade = aws.Bool(d.Get("auto_minor_version_upgrade").(bool))
	}

	if d.HasChange("backup_retention_period") {
		needsModify = true
		input.BackupRetentionPeriod = aws.Int32(int32(d.Get("backup_retention_period").(int)))
	}

	if d.HasChange("backup_window") {
		needsModify = true
		input.PreferredBackupWindow = aws.String(d.Get("backup_window").(string))
	}

	if d.HasChange("copy_tags_to_snapshot") {
		needsModify = true
		input.CopyTagsToSnapshot = aws.Bool(d.Get("copy_tags_to_snapshot").(bool))
	}

	if d.HasChange("ca_cert_identifier") {
		needsModify = true
		input.CACertificateIdentifier = aws.String(d.Get("ca_cert_identifier").(string))
	}

	if d.HasChange("customer_owned_ip_enabled") {
		needsModify = true
		input.EnableCustomerOwnedIp = aws.Bool(d.Get("customer_owned_ip_enabled").(bool))
	}

	if d.HasChange("db_subnet_group_name") {
		needsModify = true
		input.DBSubnetGroupName = aws.String(d.Get("db_subnet_group_name").(string))
	}

	if d.HasChange("deletion_protection") {
		needsModify = true
	}
	// Always set this. Fixes TestAccRDSInstance_BlueGreenDeployment_updateWithDeletionProtection
	input.DeletionProtection = aws.Bool(d.Get("deletion_protection").(bool))

	if d.HasChanges("domain", "domain_iam_role_name") {
		needsModify = true
		input.Domain = aws.String(d.Get("domain").(string))
		input.DomainIAMRoleName = aws.String(d.Get("domain_iam_role_name").(string))
	}

	if d.HasChange("enabled_cloudwatch_logs_exports") {
		needsModify = true
		oraw, nraw := d.GetChange("enabled_cloudwatch_logs_exports")
		o := oraw.(*schema.Set)
		n := nraw.(*schema.Set)

		enable := n.Difference(o)
		disable := o.Difference(n)

		input.CloudwatchLogsExportConfiguration = &types.CloudwatchLogsExportConfiguration{
			EnableLogTypes:  flex.ExpandStringValueSet(enable),
			DisableLogTypes: flex.ExpandStringValueSet(disable),
		}
	}

	if d.HasChange("iam_database_authentication_enabled") {
		needsModify = true
		input.EnableIAMDatabaseAuthentication = aws.Bool(d.Get("iam_database_authentication_enabled").(bool))
	}

	if d.HasChange("instance_class") {
		needsModify = true
		input.DBInstanceClass = aws.String(d.Get("instance_class").(string))
	}

	if d.HasChange("license_model") {
		needsModify = true
		input.LicenseModel = aws.String(d.Get("license_model").(string))
	}

	if d.HasChange("maintenance_window") {
		needsModify = true
		input.PreferredMaintenanceWindow = aws.String(d.Get("maintenance_window").(string))
	}

	if d.HasChange("max_allocated_storage") {
		needsModify = true
		v := d.Get("max_allocated_storage").(int)

		// The API expects the max allocated storage value to be set to the allocated storage
		// value when disabling autoscaling. This check ensures that value is set correctly
		// if the update to the Terraform configuration was removing the argument completely.
		if v == 0 {
			v = d.Get("allocated_storage").(int)
		}

		input.MaxAllocatedStorage = aws.Int32(int32(v))
	}

	if d.HasChange("monitoring_interval") {
		needsModify = true
		input.MonitoringInterval = aws.Int32(int32(d.Get("monitoring_interval").(int)))
	}

	if d.HasChange("monitoring_role_arn") {
		needsModify = true
		input.MonitoringRoleArn = aws.String(d.Get("monitoring_role_arn").(string))
	}

	if d.HasChange("multi_az") {
		needsModify = true
		input.MultiAZ = aws.Bool(d.Get("multi_az").(bool))
	}

	if d.HasChange("network_type") {
		needsModify = true
		input.NetworkType = aws.String(d.Get("network_type").(string))
	}

	if d.HasChange("option_group_name") {
		needsModify = true
		input.OptionGroupName = aws.String(d.Get("option_group_name").(string))
	}

	if d.HasChange("password") {
		needsModify = true
		input.MasterUserPassword = aws.String(d.Get("password").(string))
	}

	if d.HasChanges("performance_insights_enabled", "performance_insights_kms_key_id", "performance_insights_retention_period") {
		needsModify = true
		input.EnablePerformanceInsights = aws.Bool(d.Get("performance_insights_enabled").(bool))

		if v, ok := d.GetOk("performance_insights_kms_key_id"); ok {
			input.PerformanceInsightsKMSKeyId = aws.String(v.(string))
		}

		if v, ok := d.GetOk("performance_insights_retention_period"); ok {
			input.PerformanceInsightsRetentionPeriod = aws.Int32(int32(v.(int)))
		}
	}

	if d.HasChange("port") {
		needsModify = true
		input.DBPortNumber = aws.Int32(int32(d.Get("port").(int)))
	}

	if d.HasChange("publicly_accessible") {
		needsModify = true
		input.PubliclyAccessible = aws.Bool(d.Get("publicly_accessible").(bool))
	}

	if d.HasChange("replica_mode") {
		needsModify = true
		input.ReplicaMode = d.Get("replica_mode").(types.ReplicaMode)
	}

	if d.HasChange("security_group_names") {
		if v := d.Get("security_group_names").(*schema.Set); v.Len() > 0 {
			needsModify = true
			input.DBSecurityGroups = flex.ExpandStringValueSet(v)
		}
	}

	if d.HasChange("storage_throughput") {
		needsModify = true
		input.StorageThroughput = aws.Int32(int32(d.Get("storage_throughput").(int)))
	}

	if d.HasChange("storage_type") {
		needsModify = true
		input.StorageType = aws.String(d.Get("storage_type").(string))

		if aws.StringValue(input.StorageType) == storageTypeIO1 {
			input.Iops = aws.Int32(int32(d.Get("iops").(int)))
		}
	}

	if d.HasChange("vpc_security_group_ids") {
		if v := d.Get("vpc_security_group_ids").(*schema.Set); v.Len() > 0 {
			needsModify = true
			input.VpcSecurityGroupIds = flex.ExpandStringValueSet(v)
		}
	}

	return needsModify
}

func dbInstanceModify(ctx context.Context, conn *rds_sdkv2.Client, input *rds_sdkv2.ModifyDBInstanceInput, timeout time.Duration) error {
	_, err := tfresource.RetryWhenContext(ctx, timeout,
		func() (interface{}, error) {
			return conn.ModifyDBInstance(ctx, input)
		},
		func(err error) (bool, error) {
			// Retry for IAM eventual consistency.
			apiErr, ok := errs.As[smithy.APIError](err)
			if ok && apiErr.ErrorCode() == errCodeInvalidParameterValue && strings.Contains(apiErr.ErrorMessage(), "IAM role ARN value is invalid or does not include the required permissions") {
				return true, err
			}

			if errs.IsA[*types.InvalidDBClusterStateFault](err) {
				return true, err
			}

			return false, err
		},
	)

	if err != nil {
		return err
	}

	if _, err := waitDBInstanceAvailableSDKv2(ctx, conn, aws.StringValue(input.DBInstanceIdentifier), timeout); err != nil {
		return fmt.Errorf("waiting for completion: %w", err)
	}
	return nil
}

func resourceInstanceDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) (diags diag.Diagnostics) {
	conn := meta.(*conns.AWSClient).RDSConn

	input := &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier:   aws.String(d.Id()),
		DeleteAutomatedBackups: aws.Bool(d.Get("delete_automated_backups").(bool)),
	}

	if d.Get("skip_final_snapshot").(bool) {
		input.SkipFinalSnapshot = aws.Bool(true)
	} else {
		input.SkipFinalSnapshot = aws.Bool(false)

		if v, ok := d.GetOk("final_snapshot_identifier"); ok {
			input.FinalDBSnapshotIdentifier = aws.String(v.(string))
		} else {
			return errs.AppendErrorf(diags, "final_snapshot_identifier is required when skip_final_snapshot is false")
		}
	}

	log.Printf("[DEBUG] Deleting RDS DB Instance: %s", d.Id())
	_, err := conn.DeleteDBInstanceWithContext(ctx, input)

	if tfawserr.ErrMessageContains(err, errCodeInvalidParameterCombination, "disable deletion pro") {
		if v, ok := d.GetOk("deletion_protection"); (!ok || !v.(bool)) && d.Get("apply_immediately").(bool) {
			_, ierr := tfresource.RetryWhenContext(ctx, d.Timeout(schema.TimeoutUpdate),
				func() (interface{}, error) {
					return conn.ModifyDBInstanceWithContext(ctx, &rds.ModifyDBInstanceInput{
						ApplyImmediately:     aws.Bool(true),
						DBInstanceIdentifier: aws.String(d.Id()),
						DeletionProtection:   aws.Bool(false),
					})
				},
				func(err error) (bool, error) {
					// Retry for IAM eventual consistency.
					if tfawserr.ErrMessageContains(err, errCodeInvalidParameterValue, "IAM role ARN value is invalid or") {
						return true, err
					}

					// "InvalidDBInstanceState: RDS is configuring Enhanced Monitoring or Performance Insights for this DB instance. Try your request later."
					if tfawserr.ErrMessageContains(err, rds.ErrCodeInvalidDBInstanceStateFault, "your request later") {
						return true, err
					}

					return false, err
				},
			)

			if ierr != nil {
				return errs.AppendErrorf(diags, "updating RDS DB Instance (%s): %s", d.Id(), err)
			}

			if _, ierr := waitDBInstanceAvailableSDKv1(ctx, conn, d.Id(), d.Timeout(schema.TimeoutUpdate)); ierr != nil {
				return errs.AppendErrorf(diags, "waiting for RDS DB Instance (%s) update: %s", d.Id(), ierr)
			}

			_, err = conn.DeleteDBInstanceWithContext(ctx, input)
		}
	}

	if tfawserr.ErrCodeEquals(err, rds.ErrCodeDBInstanceNotFoundFault) {
		return nil
	}

	if err != nil && !tfawserr.ErrMessageContains(err, rds.ErrCodeInvalidDBInstanceStateFault, "is already being deleted") {
		return errs.AppendErrorf(diags, "deleting RDS DB Instance (%s): %s", d.Id(), err)
	}

	if _, err := waitDBInstanceDeleted(ctx, conn, d.Id(), d.Timeout(schema.TimeoutDelete)); err != nil {
		return errs.AppendErrorf(diags, "waiting for RDS DB Instance (%s) delete: %s", d.Id(), err)
	}

	return nil
}

func resourceInstanceImport(_ context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	// Neither skip_final_snapshot nor final_snapshot_identifier can be fetched
	// from any API call, so we need to default skip_final_snapshot to true so
	// that final_snapshot_identifier is not required.
	d.Set("skip_final_snapshot", true)
	d.Set("delete_automated_backups", true)
	return []*schema.ResourceData{d}, nil
}

func dbSetResourceDataEngineVersionFromInstance(d *schema.ResourceData, c *rds.DBInstance) {
	oldVersion := d.Get("engine_version").(string)
	newVersion := aws.StringValue(c.EngineVersion)
	compareActualEngineVersion(d, oldVersion, newVersion)
}

type dbInstanceARN struct {
	arn.ARN
	Identifier string
}

func parseDBInstanceARN(s string) (dbInstanceARN, error) {
	arn, err := arn.Parse(s)
	if err != nil {
		return dbInstanceARN{}, err
	}

	result := dbInstanceARN{
		ARN: arn,
	}

	re := regexp.MustCompile(`^db:([0-9a-z-]+)$`)
	matches := re.FindStringSubmatch(arn.Resource)
	if matches == nil || len(matches) != 2 {
		return dbInstanceARN{}, errors.New("DB Instance ARN: invalid resource section")
	}
	result.Identifier = matches[1]

	return result, nil
}

func findDBInstanceByIDSDKv1(ctx context.Context, conn *rds.RDS, id string) (*rds.DBInstance, error) {
	input := &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	}

	output, err := conn.DescribeDBInstancesWithContext(ctx, input)
	if tfawserr.ErrCodeEquals(err, rds.ErrCodeDBInstanceNotFoundFault) {
		return nil, &resource.NotFoundError{
			LastError:   err,
			LastRequest: input,
		}
	}
	if err != nil {
		return nil, err
	}

	if output == nil || len(output.DBInstances) == 0 || output.DBInstances[0] == nil {
		return nil, tfresource.NewEmptyResultError(input)
	}

	dbInstance := output.DBInstances[0]

	return dbInstance, nil
}

func findDBInstanceByIDSDKv2(ctx context.Context, conn *rds_sdkv2.Client, id string) (*types.DBInstance, error) {
	input := &rds_sdkv2.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(id),
	}

	output, err := conn.DescribeDBInstances(ctx, input)
	if errs.IsA[*types.DBInstanceNotFoundFault](err) {
		return nil, &resource.NotFoundError{
			LastError:   err,
			LastRequest: input,
		}
	}
	if err != nil {
		return nil, err
	}

	if output == nil || len(output.DBInstances) == 0 {
		return nil, tfresource.NewEmptyResultError(input)
	}

	return &output.DBInstances[0], nil
}

func waitDBInstanceAvailableSDKv1(ctx context.Context, conn *rds.RDS, id string, timeout time.Duration, optFns ...tfresource.OptionsFunc) (*rds.DBInstance, error) { //nolint:unparam
	options := tfresource.Options{
		PollInterval:              10 * time.Second,
		Delay:                     1 * time.Minute,
		ContinuousTargetOccurence: 3,
	}
	for _, fn := range optFns {
		fn(&options)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{
			InstanceStatusBackingUp,
			InstanceStatusConfiguringEnhancedMonitoring,
			InstanceStatusConfiguringIAMDatabaseAuth,
			InstanceStatusConfiguringLogExports,
			InstanceStatusCreating,
			InstanceStatusMaintenance,
			InstanceStatusModifying,
			InstanceStatusMovingToVPC,
			InstanceStatusRebooting,
			InstanceStatusRenaming,
			InstanceStatusResettingMasterCredentials,
			InstanceStatusStarting,
			InstanceStatusStopping,
			InstanceStatusStorageFull,
			InstanceStatusUpgrading,
		},
		Target:  []string{InstanceStatusAvailable, InstanceStatusStorageOptimization},
		Refresh: statusDBInstanceSDKv1(ctx, conn, id),
		Timeout: timeout,
	}
	options.Apply(stateConf)

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*rds.DBInstance); ok {
		return output, err
	}

	return nil, err
}

func waitDBInstanceAvailableSDKv2(ctx context.Context, conn *rds_sdkv2.Client, id string, timeout time.Duration, optFns ...tfresource.OptionsFunc) (*rds.DBInstance, error) { //nolint:unparam
	options := tfresource.Options{
		PollInterval:              10 * time.Second,
		Delay:                     1 * time.Minute,
		ContinuousTargetOccurence: 3,
	}
	for _, fn := range optFns {
		fn(&options)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{
			InstanceStatusBackingUp,
			InstanceStatusConfiguringEnhancedMonitoring,
			InstanceStatusConfiguringIAMDatabaseAuth,
			InstanceStatusConfiguringLogExports,
			InstanceStatusCreating,
			InstanceStatusMaintenance,
			InstanceStatusModifying,
			InstanceStatusMovingToVPC,
			InstanceStatusRebooting,
			InstanceStatusRenaming,
			InstanceStatusResettingMasterCredentials,
			InstanceStatusStarting,
			InstanceStatusStopping,
			InstanceStatusStorageFull,
			InstanceStatusUpgrading,
		},
		Target:  []string{InstanceStatusAvailable, InstanceStatusStorageOptimization},
		Refresh: statusDBInstanceSDKv2(ctx, conn, id),
		Timeout: timeout,
	}
	options.Apply(stateConf)

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*rds.DBInstance); ok {
		return output, err
	}

	return nil, err
}

func waitDBInstanceDeleted(ctx context.Context, conn *rds.RDS, id string, timeout time.Duration, optFns ...tfresource.OptionsFunc) (*rds.DBInstance, error) { //nolint:unparam
	options := tfresource.Options{
		PollInterval:              10 * time.Second,
		Delay:                     1 * time.Minute,
		ContinuousTargetOccurence: 3,
	}
	for _, fn := range optFns {
		fn(&options)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{
			InstanceStatusAvailable,
			InstanceStatusBackingUp,
			InstanceStatusConfiguringEnhancedMonitoring,
			InstanceStatusConfiguringLogExports,
			InstanceStatusCreating,
			InstanceStatusDeleting,
			InstanceStatusIncompatibleParameters,
			InstanceStatusIncompatibleRestore,
			InstanceStatusModifying,
			InstanceStatusStarting,
			InstanceStatusStopping,
			InstanceStatusStorageFull,
			InstanceStatusStorageOptimization,
		},
		Target:  []string{},
		Refresh: statusDBInstanceSDKv1(ctx, conn, id),
		Timeout: timeout,
	}
	options.Apply(stateConf)

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*rds.DBInstance); ok {
		return output, err
	}

	return nil, err
}

func statusDBInstanceSDKv1(ctx context.Context, conn *rds.RDS, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		output, err := findDBInstanceByIDSDKv1(ctx, conn, id)

		if tfresource.NotFound(err) {
			return nil, "", nil
		}

		if err != nil {
			return nil, "", err
		}

		return output, aws.StringValue(output.DBInstanceStatus), nil
	}
}

func statusDBInstanceSDKv2(ctx context.Context, conn *rds_sdkv2.Client, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		output, err := findDBInstanceByIDSDKv2(ctx, conn, id)

		if tfresource.NotFound(err) {
			return nil, "", nil
		}

		if err != nil {
			return nil, "", err
		}

		return output, aws.StringValue(output.DBInstanceStatus), nil
	}
}

func findBlueGreenDeploymentByID(ctx context.Context, conn *rds_sdkv2.Client, id string) (*types.BlueGreenDeployment, error) {
	input := &rds_sdkv2.DescribeBlueGreenDeploymentsInput{
		BlueGreenDeploymentIdentifier: aws.String(id),
	}

	output, err := conn.DescribeBlueGreenDeployments(ctx, input)

	if errs.IsA[*types.BlueGreenDeploymentNotFoundFault](err) {
		return nil, &resource.NotFoundError{
			LastError:   err,
			LastRequest: input,
		}
	}
	if err != nil {
		return nil, err
	}

	if output == nil || len(output.BlueGreenDeployments) == 0 {
		return nil, tfresource.NewEmptyResultError(input)
	}

	deployment := output.BlueGreenDeployments[0]

	if aws.StringValue(deployment.BlueGreenDeploymentIdentifier) != id {
		return nil, &resource.NotFoundError{
			LastRequest: input,
		}
	}

	return &deployment, nil
}

func waitBlueGreenDeploymentAvailable(ctx context.Context, conn *rds_sdkv2.Client, id string, timeout time.Duration, optFns ...tfresource.OptionsFunc) (*types.BlueGreenDeployment, error) {
	options := tfresource.Options{
		PollInterval: 10 * time.Second,
		Delay:        1 * time.Minute,
	}
	for _, fn := range optFns {
		fn(&options)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{"PROVISIONING"},
		Target:  []string{"AVAILABLE"},
		Refresh: statusBlueGreenDeployment(ctx, conn, id),
		Timeout: timeout,
	}
	options.Apply(stateConf)

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*types.BlueGreenDeployment); ok {
		return output, err
	}

	return nil, err
}

func waitBlueGreenDeploymentSwitchoverCompleted(ctx context.Context, conn *rds_sdkv2.Client, id string, timeout time.Duration, optFns ...tfresource.OptionsFunc) (*types.BlueGreenDeployment, error) {
	options := tfresource.Options{
		PollInterval: 10 * time.Second,
		Delay:        1 * time.Minute,
	}
	for _, fn := range optFns {
		fn(&options)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{"AVAILABLE", "SWITCHOVER_IN_PROGRESS"},
		Target:  []string{"SWITCHOVER_COMPLETED"},
		Refresh: statusBlueGreenDeployment(ctx, conn, id),
		Timeout: timeout,
	}
	options.Apply(stateConf)

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*types.BlueGreenDeployment); ok {
		if ues, ok := errs.As[*resource.UnexpectedStateError](err); ok {
			if ues.State == "INVALID_CONFIGURATION" || ues.State == "SWITCHOVER_FAILED" {
				err = errors.New(aws.StringValue(output.StatusDetails))
			}
		}
		return output, err
	}

	return nil, err
}

func waitBlueGreenDeploymentDeleted(ctx context.Context, conn *rds_sdkv2.Client, id string, timeout time.Duration, optFns ...tfresource.OptionsFunc) (*types.BlueGreenDeployment, error) {
	options := tfresource.Options{
		PollInterval: 10 * time.Second,
		Delay:        1 * time.Minute,
	}
	for _, fn := range optFns {
		fn(&options)
	}

	stateConf := &resource.StateChangeConf{
		Pending: []string{"PROVISIONING", "AVAILABLE", "SWITCHOVER_IN_PROGRESS", "SWITCHOVER_COMPLETED", "INVALID_CONFIGURATION", "SWITCHOVER_FAILED", "DELETING"},
		Target:  []string{},
		Refresh: statusBlueGreenDeployment(ctx, conn, id),
		Timeout: timeout,
	}
	options.Apply(stateConf)

	outputRaw, err := stateConf.WaitForStateContext(ctx)

	if output, ok := outputRaw.(*types.BlueGreenDeployment); ok {
		return output, err
	}

	return nil, err
}

func statusBlueGreenDeployment(ctx context.Context, conn *rds_sdkv2.Client, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		output, err := findBlueGreenDeploymentByID(ctx, conn, id)
		if tfresource.NotFound(err) {
			return nil, "", nil
		}
		if err != nil {
			return nil, "", err
		}

		return output, aws.StringValue(output.Status), nil
	}
}

func dbInstanceValidBlueGreenEngines() []string {
	return []string{
		InstanceEngineMariaDB,
		InstanceEngineMySQL,
	}
}
